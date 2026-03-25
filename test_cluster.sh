#!/bin/bash

# Build the binary
echo "Building paxosnode..."
go build -o paxosnode ./cmd/paxosnode

# Cleanup function to kill background processes on exit
cleanup() {
    echo "Shutting down nodes..."
    kill $PID1 $PID2 $PID3 2>/dev/null
    exit
}
trap cleanup SIGINT SIGTERM

echo "Starting 3-node cluster..."

# Start Node 1
./paxosnode --addr localhost:50051 --nodes localhost:50052,localhost:50053 > node1.log 2>&1 &
PID1=$!

# Start Node 2
./paxosnode --addr localhost:50052 --nodes localhost:50051,localhost:50053 > node2.log 2>&1 &
PID2=$!

# Start Node 3 and trigger a proposal
# We give it a unique value to propose
echo "Node 3 will propose 'Paxos-Is-Live' in 5 seconds..."
./paxosnode --addr localhost:50053 --nodes localhost:50051,localhost:50052 --propose "Paxos-Is-Live" > node3.log 2>&1 &
PID3=$!

echo "Nodes are running. Monitoring logs for SUCCESS..."
echo "Press Ctrl+C to stop the cluster."

# Tail the logs to show progress
tail -f node1.log node2.log node3.log | grep --line-buffered -E "Node starting|Connected|SUCCESS|Decided"
