package raft

import "time"

type LogEntry struct {
	Term         int         // 该日志条目的任期
	CommandValid bool        // 是否有效
	Command      interface{} // 应用于状态机的命令
}

type AppendEntriesArgs struct {
	Term     int
	LeaderId int

	// used to probe the match point
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []LogEntry
}

type AppendEntriesReply struct {
	Term    int
	Success bool
}

// Peer 的回调函数
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply.Term = rf.currentTerm
	reply.Success = false

	// 对齐任期
	if args.Term < rf.currentTerm {
		LOG(rf.me, rf.currentTerm, DLog2, "Reject log, Higher term, T%d<T%d", args.LeaderId, args.Term, rf.currentTerm)
		return
	}
	if args.Term >= rf.currentTerm {
		rf.becomeFollwerLocked(args.Term)
	}

	// 如果 prevLog 不匹配，则返回
	if args.PrevLogIndex > len(rf.log) {
		LOG(rf.me, rf.currentTerm, DLog2, "<- S%d, Reject log, Follower log too short, Len: %d < Prev: %d", args.LeaderId, len(rf.log), args.PrevLogIndex)
		return
	}
	if rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
		LOG(rf.me, rf.currentTerm, DLog2, "<- S%d, Reject log, Follower log, Prev log not match, [%d]: T%d != T%d", args.LeaderId, args.PrevLogIndex, rf.log[args.PrevLogIndex].Term, args.PrevLogTerm)
		return
	}

	// 将领导者日志条目添加到本地
	rf.log = append(rf.log[:args.PrevLogIndex+1], args.Entries...)
	reply.Success = true
	LOG(rf.me, rf.currentTerm, DLog2, "Follower accept logs: (%d, %d]", args.PrevLogIndex, args.PrevLogIndex+len(args.Entries))

	// TODO:  处理领导 Commit

	rf.resetElectionTimerLocked()
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

func (rf *Raft) startReplication(term int) bool {
	replicateToPeer := func(peer int, args *AppendEntriesArgs) {
		reply := &AppendEntriesReply{}
		ok := rf.sendAppendEntries(peer, args, reply)

		rf.mu.Lock()
		defer rf.mu.Unlock()
		if !ok {
			LOG(rf.me, rf.currentTerm, DLog, "-> S%d, Lost or crashed", peer)
			return
		}

		// 对齐任期
		if reply.Term > rf.currentTerm {
			rf.becomeFollwerLocked(reply.Term)
			return
		}

		// 处理 rpc 返回值
		// 如果 prevLog 不匹配，则回撤
		if !reply.Success {
			// go back a term
			idx, term := args.PrevLogIndex, args.PrevLogTerm
			for idx > 0 && rf.log[idx].Term == term {
				idx--
			}
			rf.nextIndex[peer] = idx + 1
			LOG(rf.me, rf.currentTerm, DLog, "Not match with S%d in %d, try next=%d", peer, args.PrevLogIndex, rf.nextIndex[peer])
			return
		}

		// 如果日志添加成功，更新/匹配下一个索引
		rf.matchIndex[peer] = args.PrevLogIndex + len(args.Entries)	// important
		rf.nextIndex[peer] = rf.matchIndex[peer] + 1

		// TODO: 更新 Commit 索引
	}

	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.contextLostLocked(Leader, term) {
		LOG(rf.me, rf.currentTerm, DLog, "Lost leader[%d] to %s[T%d]", term, rf.role, rf.currentTerm)
		return false
	}

	for peer := 0; peer < len(rf.peers); peer++ {
		if peer == rf.me {
			rf.matchIndex[peer] = len(rf.log) - 1
			rf.nextIndex[peer] = len(rf.log)
			continue
		}

		prevIdx := rf.nextIndex[peer] - 1
		prevTerm := rf.log[prevIdx].Term
		args := &AppendEntriesArgs{
			Term:         rf.currentTerm,
			LeaderId:     rf.me,
			PrevLogIndex: prevIdx,
			PrevLogTerm:  prevTerm,
			Entries:      rf.log[prevIdx+1:],
		}
		go replicateToPeer(peer, args)
	}

	return true
}

// could only replcate in the given term
func (rf *Raft) replicationTicker(term int) {
	for !rf.killed() {
		ok := rf.startReplication(term)
		if !ok {
			break
		}

		time.Sleep(replicateInterval)
	}
}
