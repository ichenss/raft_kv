package raft

import "log"

type logTopic string
const (
	DLog logTopic = "log"
	DError logTopic = "error"
	DVote logTopic = "vote"
	DLeader logTopic = "leader"
	DDebug logTopic = "debug"
	DLog2 logTopic = "log2"
)

// Debugging
const Debug = false

func DPrintf(format string, a ...interface{}) {
	if Debug {
		log.Printf(format, a...)
	}
}

func LOG(peerId  int, term int, topic logTopic, format string, a ...interface{}) {
	// TODO
}