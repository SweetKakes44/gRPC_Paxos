package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"testing"
	"time"

	pb "gRPC_Paxos/gen/paxosv1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func startTestServer(t *testing.T, addr string, peers []string) *grpc.Server {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	paxosSrv := newPaxosServer(addr, peers)
	pb.RegisterPaxosServer(s, paxosSrv)
	go func() {
		if err := s.Serve(lis); err != nil {
			log.Printf("server served with error: %v", err)
		}
	}()
	return s
}

func TestPaxosAgreement(t *testing.T) {
	addrs := []string{"localhost:50051", "localhost:50052", "localhost:50053"}
	servers := make([]*grpc.Server, len(addrs))

	for i, addr := range addrs {
		servers[i] = startTestServer(t, addr, addrs)
	}
	defer func() {
		for _, s := range servers {
			s.Stop()
		}
	}()

	time.Sleep(100 * time.Millisecond) // Wait for servers to start

	// Propose from multiple nodes
	var wg sync.WaitGroup
	results := make(chan string, len(addrs))

	for i, addr := range addrs {
		wg.Add(1)
		go func(a string, val string) {
			defer wg.Done()
			conn, err := grpc.Dial(a, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Errorf("failed to dial: %v", err)
				return
			}
			defer conn.Close()
			client := pb.NewPaxosClient(conn)
			resp, err := client.ProposeValue(context.Background(), &pb.ProposeValueRequest{Value: val})
			if err != nil {
				t.Errorf("failed to propose: %v", err)
				return
			}
			results <- resp.DecidedValue
		}(addr, fmt.Sprintf("val-%d", i))
	}

	wg.Wait()
	close(results)

	var firstDecided string
	for res := range results {
		if firstDecided == "" {
			firstDecided = res
		} else if firstDecided != res {
			t.Errorf("Agreement failed: got %s and %s", firstDecided, res)
		}
	}
	t.Logf("Successfully agreed on: %s", firstDecided)
}
