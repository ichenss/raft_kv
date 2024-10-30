package raft

import (
	"sort"
	"time"
)

type LogEntry struct {
	Term         int         // 日志条目的任期号
	CommandValid bool        // 命令是否有效
	Command      interface{} // 实际命令内容
}

type AppendEntriesArgs struct {
	Term     int	// 领导者的任期
	LeaderId int	// 领导者ID

	// 用于探测匹配点
	PrevLogIndex int		// 前一个日志条目的索引
	PrevLogTerm  int		// 前一个日志条目的任期
	Entries      []LogEntry	// 需要追加的日志条目

	// 用于更新追随者的 commitIndex
	LeaderCommit int	// 领导者的 commitIndex
}

type AppendEntriesReply struct {
	Term    int		// 当前任期
	Success bool	// 是否成功

	ConfilictIndex int	// 冲突的索引
	ConfilictTerm  int	// 冲突的任期
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
	if args.PrevLogIndex >= len(rf.log) {
		reply.ConfilictIndex = len(rf.log)
		reply.ConfilictTerm = InvalidTerm
		LOG(rf.me, rf.currentTerm, DLog2, "<- S%d, Reject log, Follower log too short, Len: %d < Prev: %d", args.LeaderId, len(rf.log), args.PrevLogIndex)
		return
	}
	if rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
		reply.ConfilictTerm = rf.log[args.PrevLogIndex].Term
		reply.ConfilictIndex = rf.firstLogFor(reply.ConfilictTerm)
		LOG(rf.me, rf.currentTerm, DLog2, "<- S%d, Reject log, Follower log, Prev log not match, [%d]: T%d != T%d", args.LeaderId, args.PrevLogIndex, rf.log[args.PrevLogIndex].Term, args.PrevLogTerm)
		return
	}

	// 将领导者日志条目添加到本地
	rf.log = append(rf.log[:args.PrevLogIndex+1], args.Entries...)
	rf.persistLocked()
	reply.Success = true
	LOG(rf.me, rf.currentTerm, DLog2, "Follower accept logs: (%d, %d]", args.PrevLogIndex, args.PrevLogIndex+len(args.Entries))

	// 处理领导者 Commit
	if args.LeaderCommit > rf.commitIndex {
		LOG(rf.me, rf.currentTerm, DApply, "Follower the commit index %d->%d", rf.commitIndex, args.LeaderCommit)
		rf.commitIndex = args.LeaderCommit
		rf.applyCond.Signal()
	}

	rf.resetElectionTimerLocked()
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

func (rf *Raft) getMajorityIndexLocked() int {
	tmpIndexes := make([]int, len(rf.peers))
	copy(tmpIndexes, rf.matchIndex)
	sort.Ints(sort.IntSlice(tmpIndexes))
	majorityIdx := (len(rf.peers) - 1) / 2
	LOG(rf.me, rf.currentTerm, DDebug, "Match index after sort: %v, majority[%d]=%d", tmpIndexes, majorityIdx, tmpIndexes[majorityIdx])
	return tmpIndexes[majorityIdx]
}

// 仅在给定的 term 中有效
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

		// 检查 context
		if rf.contextLostLocked(Leader, term) {
			LOG(rf.me, rf.currentTerm, DLog, "-> S%d, Context Lost, T%d:Leader->T%d:%d", peer, term, rf.currentTerm, rf.role)
			return
		}

		// 处理 rpc 返回值
		// 如果日志不匹配，回退nextIndex
		if !reply.Success {
			// go back a term
			idx, term := args.PrevLogIndex, args.PrevLogTerm
			for idx > 0 && rf.log[idx].Term == term {
				idx--
			}
			rf.nextIndex[peer] = idx + 1
			LOG(rf.me, rf.currentTerm, DLog, "-> S%d, Not matched at %d, try next=%d", peer, args.PrevLogIndex, rf.nextIndex[peer])
			return
		}

		// 如果日志添加成功，更新/匹配下一个索引
		rf.matchIndex[peer] = args.PrevLogIndex + len(args.Entries) // important
		rf.nextIndex[peer] = rf.matchIndex[peer] + 1

		// 更新 commitIndex
		majorityMatched := rf.getMajorityIndexLocked()
		if majorityMatched > rf.commitIndex {
			LOG(rf.me, rf.currentTerm, DApply, "Leader the commit index %d->%d", rf.commitIndex, majorityMatched)
			rf.commitIndex = majorityMatched
			rf.applyCond.Signal()
		}
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
			LeaderCommit: rf.commitIndex,
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
