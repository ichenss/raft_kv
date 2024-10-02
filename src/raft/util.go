package raft

import "log"

// Debugging
const Debug = false

func DPrintf(format string, a ...interface{}) {
	if Debug {
		log.Printf(format, a...)
	}
}

func LOG(peerId  int, term int, topic logTopic, format string, a ...interface{}) {
	
}