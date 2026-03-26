package paxos

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	pb "paxos-grpc/gen/paxosv1"
)

// Node implements the Paxos gRPC service.
type Node struct {
	pb.UnimplementedPaxosServer

	mu sync.Mutex

	// Acceptor state
	promisedId    *pb.ProposalId
	acceptedId    *pb.ProposalId
	acceptedValue string

	// Learner state
	decidedValue string

	// Proposer state
	address      string
	nextSequence uint64
	nodes        []pb.PaxosClient // Does NOT include self
}

// NewNode initializes a node with its unique address.
func NewNode(address string) *Node {
	return &Node{
		address:      address,
		nextSequence: 1,
	}
}

// SetNodes updates the list of remote gRPC nodes.
func (n *Node) SetNodes(nodes []pb.PaxosClient) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nodes = nodes
}

// Decide implements Phase 3 of Paxos (Learner role).
func (n *Node) Decide(ctx context.Context, req *pb.DecideRequest) (*pb.DecideResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.decidedValue = req.Value
	return &pb.DecideResponse{Acknowledged: true}, nil
}

// ProposeValue is the entry point for clients.
// It runs the full Paxos algorithm to try and get a value chosen.
func (n *Node) ProposeValue(ctx context.Context, req *pb.ProposeValueRequest) (*pb.ProposeValueResponse, error) {
	for {
		// 1. Prepare Phase
		proposalId := n.generateId()

		// Collect promises from self and other nodes
		promises := n.sendPrepare(ctx, proposalId)

		if !n.hasQuorum(len(promises)) {
			fmt.Printf("--> FAILED to get quorum (only %d/%d nodes). Retrying in 1s...\n", len(promises), len(n.nodes)+1)
			time.Sleep(1 * time.Second) // Prevent a "hot loop" that freezes your terminal
			continue
		}
		fmt.Printf("--> SUCCESS: Quorum reached with %d nodes. Moving to Phase 2 (Accept)\n", len(promises))
		// ... rest of your code ...
		// 2. Accept Phase
		// Pick the value to propose:
		// If any acceptor returned a previously accepted value, we MUST use the one with the highest ID.
		valueToPropose := req.Value
		var highestAcceptedId *pb.ProposalId

		for _, p := range promises {
			if p.HasAccepted {
				if CompareProposalIds(p.AcceptedId, highestAcceptedId) > 0 {
					highestAcceptedId = p.AcceptedId
					valueToPropose = p.AcceptedValue
				}
			}
		}

		acceptances := n.sendAccept(ctx, proposalId, valueToPropose)
		if !n.hasQuorum(len(acceptances)) {
			continue
		}

		// 3. Decide Phase
		// Quorum reached! Value is chosen.
		n.broadcastDecide(ctx, proposalId, valueToPropose)

		return &pb.ProposeValueResponse{DecidedValue: valueToPropose}, nil
	}
}

func (n *Node) generateId() *pb.ProposalId {
	n.mu.Lock()
	defer n.mu.Unlock()
	id := &pb.ProposalId{
		Sequence: n.nextSequence,
		Address:  n.address,
	}
	n.nextSequence++
	return id
}

func (n *Node) hasQuorum(count int) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	totalNodes := len(n.nodes) + 1 // +1 for self
	return count >= (totalNodes/2)+1
}

func (n *Node) sendPrepare(ctx context.Context, id *pb.ProposalId) []*pb.PrepareResponse {
	var responses []*pb.PrepareResponse
	var mu sync.Mutex
	var wg sync.WaitGroup

	// 1. Ask self
	res, _ := n.Prepare(ctx, &pb.PrepareRequest{ProposalId: id})
	if res != nil && res.Promised {
		responses = append(responses, res)
	}

	n.mu.Lock()
	nodes := n.nodes
	n.mu.Unlock()

	for _, node := range nodes {
		wg.Add(1)
		// We pass the client 'p' into the goroutine
		go func(p pb.PaxosClient) {
			defer wg.Done()

			// Use MILLISECONDS so the network has time to respond
			childCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()

			resp, err := p.Prepare(childCtx, &pb.PrepareRequest{ProposalId: id})

			if err != nil {
				// This will show up in your Zellij pane for the dead node
				fmt.Printf("--> [NETWORK ERROR] A node is unreachable: %v", err)
				return
			}

			if resp != nil && resp.Promised {
				mu.Lock()
				responses = append(responses, resp)
				mu.Unlock()
			}
		}(node) // Pass the node client here
	}

	wg.Wait()
	return responses
}

func (n *Node) sendAccept(ctx context.Context, id *pb.ProposalId, value string) []*pb.AcceptResponse {
	var responses []*pb.AcceptResponse
	var mu sync.Mutex
	var wg sync.WaitGroup

	// 1. Ask self
	res, _ := n.Accept(ctx, &pb.AcceptRequest{ProposalId: id, Value: value})
	if res != nil && res.Accepted {
		responses = append(responses, res)
	}

	n.mu.Lock()
	nodes := n.nodes
	n.mu.Unlock()

	for _, node := range nodes {
		wg.Add(1)
		go func(p pb.PaxosClient) {
			defer wg.Done()

			childCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()

			resp, err := p.Accept(childCtx, &pb.AcceptRequest{ProposalId: id, Value: value})
			if err == nil && resp != nil && resp.Accepted {
				mu.Lock()
				responses = append(responses, resp)
				mu.Unlock()
			}
		}(node)
	}
	wg.Wait()
	return responses
}

func (n *Node) broadcastDecide(ctx context.Context, id *pb.ProposalId, value string) {
	// Update self
	n.Decide(ctx, &pb.DecideRequest{ProposalId: id, Value: value})

	// Inform other nodes
	n.mu.Lock()
	nodes := n.nodes
	n.mu.Unlock()

	for _, node := range nodes {
		go func(p pb.PaxosClient) {
			p.Decide(ctx, &pb.DecideRequest{ProposalId: id, Value: value})
		}(node)
	}
}

// CompareProposalIds returns:
//
//	1 if a > b
//
// -1 if a < b
//
//	0 if a == b
//
// Rule:
// 1) higher sequence wins
// 2) if sequence ties, lexicographically higher address wins
func CompareProposalIds(a, b *pb.ProposalId) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}

	if a.Sequence > b.Sequence {
		return 1
	}
	if a.Sequence < b.Sequence {
		return -1
	}

	if a.Address > b.Address {
		return 1
	}
	if a.Address < b.Address {
		return -1
	}

	return 0
}

// Prepare implements Phase 1 of Paxos (Acceptor role).
// It promises not to accept any future proposals with an ID lower than the one requested.
func (n *Node) Prepare(ctx context.Context, req *pb.PrepareRequest) (*pb.PrepareResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	res := &pb.PrepareResponse{
		Promised: false,
	}

	// Rule: Promise only if the incoming ID is strictly higher than any promised ID.
	if CompareProposalIds(req.ProposalId, n.promisedId) > 0 {
		n.promisedId = req.ProposalId
		res.Promised = true
	}

	res.PromisedId = n.promisedId

	// If the acceptor has already accepted a value, it must return it to the proposer.
	if n.acceptedId != nil {
		res.HasAccepted = true
		res.AcceptedId = n.acceptedId
		res.AcceptedValue = n.acceptedValue
	}

	return res, nil
}

// Accept implements Phase 2 of Paxos (Acceptor role).
// It accepts a proposal only if it hasn't promised a higher ID since Phase 1.
func (n *Node) Accept(ctx context.Context, req *pb.AcceptRequest) (*pb.AcceptResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	res := &pb.AcceptResponse{
		Accepted: false,
	}

	// Rule: Accept only if the ID is >= promised ID.
	if CompareProposalIds(req.ProposalId, n.promisedId) >= 0 {
		n.promisedId = req.ProposalId // Always keep promisedId current
		n.acceptedId = req.ProposalId
		n.acceptedValue = req.Value
		res.Accepted = true
	}

	res.PromisedId = n.promisedId
	return res, nil
}
