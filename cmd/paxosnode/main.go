package main

import (
	"context"
	"flag"
	"log"
	"net"
	"strings"
	"time"

	pb "paxos-grpc/gen/paxosv1"
	"paxos-grpc/internal/paxos"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("addr", "localhost:50051", "Address to listen on")
	nodesList := flag.String("nodes", "", "Comma-separated list of other node addresses")
	proposeValue := flag.String("propose", "", "Value to propose after startup (optional)")
	flag.Parse()

	// 1. Start gRPC Server
	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	node := paxos.NewNode(*addr)
	pb.RegisterPaxosServer(s, node)

	log.Printf("Node starting at %s", *addr)

	// 2. Connect to other nodes in background
	go func() {
		if *nodesList == "" {
			return
		}

		nodeAddrs := strings.Split(*nodesList, ",")
		var remoteNodes []pb.PaxosClient

		// Wait a bit for other nodes to start
		time.Sleep(2 * time.Second)

		for _, nAddr := range nodeAddrs {
			conn, err := grpc.NewClient(nAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				log.Printf("could not connect to node %s: %v", nAddr, err)
				continue
			}
			remoteNodes = append(remoteNodes, pb.NewPaxosClient(conn))
			log.Printf("Connected to node: %s", nAddr)
		}
		node.SetNodes(remoteNodes)

		// 3. Optional: Propose a value if requested
		if *proposeValue != "" {
			time.Sleep(2 * time.Second) // Wait for all connections to stabilize
			log.Printf("Proposing value: %s", *proposeValue)
			resp, err := node.ProposeValue(context.Background(), &pb.ProposeValueRequest{
				Value: *proposeValue,
			})
			if err != nil {
				log.Printf("Proposal failed: %v", err)
			} else {
				log.Printf("SUCCESS! Decided value: %s", resp.DecidedValue)
			}
		}
	}()

	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
