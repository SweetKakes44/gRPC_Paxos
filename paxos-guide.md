# gRPC Paxos Implementation Guide

This document summarizes the Paxos implementation using gRPC, as discussed in the session on March 23, 2026.

---

## 1. Project Overview
The project implements a **Single-Decree Paxos** consensus algorithm using Go and gRPC. Every node in the cluster acts as a Proposer, Acceptor, and Learner simultaneously.

### Key Files
- `paxos.proto`: Defines the gRPC service and message structures (Prepare, Accept, Decide).
- `gRPC-paxos.go`: The core implementation of the Paxos node logic.
- `paxos_test.go`: Automated test suite to verify agreement and quorum handling.
- `go.mod`: Go module configuration.

---

## 2. The Paxos Workflow (The "Big Picture")

### Phase 1: Prepare (Promise)
- **Proposer**: Generates a unique, higher `ProposalId` (Sequence + Address) and broadcasts it to a quorum of nodes.
- **Acceptor**: If the incoming ID is higher than its current `promisedId`, it promises not to accept lower IDs and returns any previously accepted value.
- **Goal**: Establish a "lock" on a quorum and learn if a value was already chosen.

### Phase 2: Accept (Accepted)
- **Proposer**: If it receives promises from a majority, it picks a value (either its own or one returned by an acceptor) and asks the quorum to accept it.
- **Acceptor**: If the proposal ID is still valid (hasn't been superseded), the node "votes" for this value.
- **Goal**: Get a majority to vote for a specific value.

### Phase 3: Decide (Learn)
- **Proposer**: Once a majority has accepted the value, it's officially "chosen." The proposer then broadcasts this final decision to all nodes.
- **Learner**: All nodes update their local state to the chosen value and respond to the client.
- **Goal**: Finalize the decision across the cluster.

---

## 3. Function-by-Function Breakdown

### Core Logic & State Management
- `newPaxosServer`: Initializes a node with its address and the list of cluster peers.
- `compareProposalIds`: Implements deterministic tie-breaking (Sequence first, then Address).
- `ProposeValue`: The main entry point for clients; runs the Paxos retry loop until a value is decided.

### RPC Handlers (Acceptor/Learner Roles)
- `Prepare`: Validates and promises to follow a proposer's ID.
- `Accept`: Records a vote for a proposal if it's still the highest one promised.
- `Decide`: Updates the local "decided" state once consensus is reached.

### Network Communication (Proposer Role)
- `broadcastPrepare / broadcastAccept / broadcastDecide`: Uses Go routines to send RPCs to all peers in parallel.
- `callPrepare / callAccept / callDecide`: Helper functions that handle the gRPC connection logic for both local and remote calls.

---

## 4. Usage Instructions

### Running the Server
To start a node, specify its address and the list of all peers (including itself):
```bash
go run gRPC-paxos.go --address localhost:50051 --peers localhost:50051,localhost:50052,localhost:50053
```

### Verifying the Implementation
Run the automated agreement test:
```bash
go test -v .
```

---

## 5. Security & Safety Notes
- **Quorums**: The system requires a majority (`n/2 + 1`) of nodes to be online to reach consensus.
- **Liveness**: The retry loop in `ProposeValue` includes a small sleep to prevent "livelock" where two proposers constantly override each other's IDs.
