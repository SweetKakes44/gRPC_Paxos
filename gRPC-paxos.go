package main

import (
	"context"
	"flag"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	pb "gRPC_Paxos/gen/paxosv1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type paxosServer struct {
	pb.UnimplementedPaxosServer
	mu sync.Mutex

	// Node config
	address string
	peers   []string

	// Acceptor state
	promisedId    *pb.ProposalId
	acceptedId    *pb.ProposalId
	acceptedValue string

	// Learner state
	decidedValue string

	// Proposer state
	currentSeq uint64
}

func newPaxosServer(address string, peers []string) *paxosServer {
	return &paxosServer{
		address: address,
		peers:   peers,
	}
}

// Comparison rule (deterministic):
// 1) higher sequence wins
// 2) if sequence ties, lexicographically higher address wins
func compareProposalIds(id1, id2 *pb.ProposalId) int {
	if id1 == nil && id2 == nil {
		return 0
	}
	if id1 == nil {
		return -1
	}
	if id2 == nil {
		return 1
	}

	if id1.Sequence > id2.Sequence {
		return 1
	}
	if id1.Sequence < id2.Sequence {
		return -1
	}
	if id1.Address > id2.Address {
		return 1
	}
	if id1.Address < id2.Address {
		return -1
	}
	return 0
}

func (s *paxosServer) Prepare(ctx context.Context, req *pb.PrepareRequest) (*pb.PrepareResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if compareProposalIds(req.ProposalId, s.promisedId) > 0 {
		s.promisedId = req.ProposalId
		return &pb.PrepareResponse{
			Promised:      true,
			PromisedId:    s.promisedId,
			HasAccepted:   s.acceptedId != nil,
			AcceptedId:    s.acceptedId,
			AcceptedValue: s.acceptedValue,
		}, nil
	}

	return &pb.PrepareResponse{
		Promised:   false,
		PromisedId: s.promisedId,
	}, nil
}

func (s *paxosServer) Accept(ctx context.Context, req *pb.AcceptRequest) (*pb.AcceptResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Accept if proposal_id >= promised_id
	if compareProposalIds(req.ProposalId, s.promisedId) >= 0 {
		s.promisedId = req.ProposalId
		s.acceptedId = req.ProposalId
		s.acceptedValue = req.Value
		return &pb.AcceptResponse{
			Accepted:   true,
			PromisedId: s.promisedId,
		}, nil
	}

	return &pb.AcceptResponse{
		Accepted:   false,
		PromisedId: s.promisedId,
	}, nil
}

func (s *paxosServer) Decide(ctx context.Context, req *pb.DecideRequest) (*pb.DecideResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.decidedValue = req.Value
	log.Printf("[%s] Decided value: %s", s.address, s.decidedValue)

	return &pb.DecideResponse{
		Acknowledged: true,
	}, nil
}

func (s *paxosServer) ProposeValue(ctx context.Context, req *pb.ProposeValueRequest) (*pb.ProposeValueResponse, error) {
	for {
		s.mu.Lock()
		if s.decidedValue != "" {
			val := s.decidedValue
			s.mu.Unlock()
			return &pb.ProposeValueResponse{DecidedValue: val}, nil
		}
		s.currentSeq++
		propId := &pb.ProposalId{
			Sequence: s.currentSeq,
			Address:  s.address,
		}
		s.mu.Unlock()

		// Phase 1: Prepare
		promises := s.broadcastPrepare(propId)
		if len(promises) <= len(s.peers)/2 {
			log.Printf("[%s] Failed to get quorum for Prepare (id: %v)", s.address, propId)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Pick value
		valueToPropose := req.Value
		var maxAcceptedId *pb.ProposalId
		for _, p := range promises {
			if p.HasAccepted && compareProposalIds(p.AcceptedId, maxAcceptedId) > 0 {
				maxAcceptedId = p.AcceptedId
				valueToPropose = p.AcceptedValue
			}
		}

		// Phase 2: Accept
		accepts := s.broadcastAccept(propId, valueToPropose)
		if len(accepts) <= len(s.peers)/2 {
			log.Printf("[%s] Failed to get quorum for Accept (id: %v)", s.address, propId)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Phase 3: Decide
		s.broadcastDecide(propId, valueToPropose)

		return &pb.ProposeValueResponse{DecidedValue: valueToPropose}, nil
	}
}

func (s *paxosServer) broadcastPrepare(propId *pb.ProposalId) []*pb.PrepareResponse {
	var mu sync.Mutex
	var responses []*pb.PrepareResponse
	var wg sync.WaitGroup

	for _, peer := range s.peers {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			resp, err := s.callPrepare(p, propId)
			if err == nil && resp.Promised {
				mu.Lock()
				responses = append(responses, resp)
				mu.Unlock()
			}
		}(peer)
	}
	wg.Wait()
	return responses
}

func (s *paxosServer) broadcastAccept(propId *pb.ProposalId, value string) []*pb.AcceptResponse {
	var mu sync.Mutex
	var responses []*pb.AcceptResponse
	var wg sync.WaitGroup

	for _, peer := range s.peers {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			resp, err := s.callAccept(p, propId, value)
			if err == nil && resp.Accepted {
				mu.Lock()
				responses = append(responses, resp)
				mu.Unlock()
			}
		}(peer)
	}
	wg.Wait()
	return responses
}

func (s *paxosServer) broadcastDecide(propId *pb.ProposalId, value string) {
	var wg sync.WaitGroup
	for _, peer := range s.peers {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			s.callDecide(p, propId, value)
		}(peer)
	}
	wg.Wait()
}

func (s *paxosServer) callPrepare(peer string, propId *pb.ProposalId) (*pb.PrepareResponse, error) {
	if peer == s.address {
		return s.Prepare(context.Background(), &pb.PrepareRequest{ProposalId: propId})
	}
	conn, err := grpc.Dial(peer, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	client := pb.NewPaxosClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return client.Prepare(ctx, &pb.PrepareRequest{ProposalId: propId})
}

func (s *paxosServer) callAccept(peer string, propId *pb.ProposalId, value string) (*pb.AcceptResponse, error) {
	if peer == s.address {
		return s.Accept(context.Background(), &pb.AcceptRequest{ProposalId: propId, Value: value})
	}
	conn, err := grpc.Dial(peer, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	client := pb.NewPaxosClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return client.Accept(ctx, &pb.AcceptRequest{ProposalId: propId, Value: value})
}

func (s *paxosServer) callDecide(peer string, propId *pb.ProposalId, value string) (*pb.DecideResponse, error) {
	if peer == s.address {
		return s.Decide(context.Background(), &pb.DecideRequest{ProposalId: propId, Value: value})
	}
	conn, err := grpc.Dial(peer, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	client := pb.NewPaxosClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return client.Decide(ctx, &pb.DecideRequest{ProposalId: propId, Value: value})
}

func main() {
	address := flag.String("address", "localhost:50051", "The server address")
	peersStr := flag.String("peers", "localhost:50051", "Comma-separated list of peer addresses")
	flag.Parse()

	peers := strings.Split(*peersStr, ",")

	lis, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	paxosSrv := newPaxosServer(*address, peers)
	pb.RegisterPaxosServer(s, paxosSrv)

	log.Printf("Paxos server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
