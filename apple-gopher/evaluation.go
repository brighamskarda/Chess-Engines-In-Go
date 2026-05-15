// Copyright (C) 2026 Brigham Skarda
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"context"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"time"

	"github.com/brighamskarda/chess/v2"
	"github.com/brighamskarda/chess/v2/uci"
)

// evalRequest is sent to a worker thread so it know what it should do.
type evalRequest struct {
	// ctx being cancelled means the search should stop.
	ctx      context.Context
	position *chess.Position
	response chan evalResponse
	// depth is the number of plys deep to search.
	//
	// 0 is invalid.
	depth uint8
}

// evalResponse is the response that a worker thread will provide in response to a search.
type evalResponse struct {
	primaryVariation []chess.Move
	nodes            uint
	nps              uint
	score            int16
	valid            bool
}

type workerThread struct {
	engine        *engine
	nodesSearched uint
	stop          bool
}

// start will read eval requests and perform the requested searches until the channel is closed.
func (worker *workerThread) start(requests <-chan evalRequest) {
	for req := range requests {
		// Check depth bounds
		if req.depth > maxDepth {
			worker.engine.sendStringMsgBlock(fmt.Sprintf("Error, requested a searched depth greater than %d which is the maximum this engine supports.", maxDepth))
			worker.engine.cancel()
		}
		worker.nodesSearched = 0
		worker.stop = false

		// Perform evaluation
		startTime := time.Now()
		score := worker.alphaBetaSearch(req.ctx, req.position, req.position.SideToMove == chess.White, math.MinInt16, math.MaxInt16, req.depth, calculateHash(req.position))
		pv := worker.getPrimaryVariation(req.position, req.depth)
		secondsElapsed := float64(time.Since(startTime)) / float64(time.Second)

		// Send response
		req.response <- evalResponse{
			primaryVariation: pv,
			nodes:            worker.nodesSearched,
			nps:              uint(float64(worker.nodesSearched) / secondsElapsed),
			score:            score,
			valid:            req.ctx.Err() == nil,
		}
		worker.engine.evalResponsePool.Put(req.response)
	}
}

// alphaBetaSearch https://en.wikipedia.org/wiki/Alpha%E2%80%93beta_pruning#Pseudocode
func (worker *workerThread) alphaBetaSearch(ctx context.Context, pos *chess.Position, maximizingPlayer bool,
	a int16, b int16, depth uint8, currentZobr uint64) int16 {
	worker.nodesSearched++
	if worker.nodesSearched&0x7FF == 0 && ctx.Err() != nil {
		worker.stop = true
	}

	// 1. Probe the Transposition Table
	hashedValue, ok := worker.engine.getHash(currentZobr)
	if ok && hashedValue.depth >= depth {
		// Use the bounds correctly
		if hashedValue.nodeType == nodeExact {
			return hashedValue.score
		} else if hashedValue.nodeType == nodeAlphaBound && hashedValue.score <= a {
			return a // Alpha cutoff
		} else if hashedValue.nodeType == nodeBetaBound && hashedValue.score >= b {
			return b // Beta cutoff
		}
	}

	// reached desired depth
	if depth == 0 {
		return worker.quiescenceSearch(ctx, pos, maximizingPlayer, a, b)
	}

	// no possible moves
	legalMoves := chess.LegalMoves(pos)
	if len(legalMoves) == 0 {
		return handleTerminalNode(pos, depth)
	}

	// --- TT Move Ordering ---
	// If we found an entry in the TT (even if depth was too low for a cutoff),
	// it contains the best move from a previous shallower search.
	// We want to evaluate that move FIRST.
	if ok {
		ttMove := hashedValue.bestMove()
		if ttMove != (chess.Move{}) {
			for i, move := range legalMoves {
				if move == ttMove {
					// Swap the TT move to index 0
					legalMoves[0], legalMoves[i] = legalMoves[i], legalMoves[0]
					break
				}
			}
		}
	}
	// ------------------------

	if maximizingPlayer {
		return worker.maximize(ctx, legalMoves, pos, a, b, depth, currentZobr)
	} else {
		return worker.minimize(ctx, legalMoves, pos, a, b, depth, currentZobr)
	}
}
func (worker *workerThread) maximize(ctx context.Context, legalMoves []chess.Move, pos *chess.Position,
	a int16, b int16, depth uint8, currentZobr uint64) int16 {
	newPos := worker.engine.positionPool.Get().(*chess.Position)
	defer worker.engine.positionPool.Put(newPos)
	value := int16(math.MinInt16)
	oldA := a
	oldB := b
	bestMove := chess.Move{}

	for _, move := range legalMoves {
		*newPos = *pos
		newZobr := updateHash(currentZobr, newPos, move)
		newPosScore := worker.alphaBetaSearch(ctx, newPos, false, a, b, depth-1, newZobr)

		if worker.stop {
			break
		}

		if newPosScore > value {
			value = newPosScore
			bestMove = move
		}

		a = max(a, value)
		if value >= b {
			break
		}
	}

	if !worker.stop {
		// Determine Node Type for Maximize
		var nType uint8
		if value >= oldB {
			nType = nodeBetaBound // Lower Bound (Value is at least Beta)
		} else if value <= oldA {
			nType = nodeAlphaBound
		} else {
			nType = nodeExact
		}

		// Store in TT
		hash := hashEntry{
			hash:               currentZobr,
			score:              value,
			depth:              depth,
			bestMoveFromSquare: compressSquare(bestMove.FromSquare),
			bestMoveToSquare:   compressSquare(bestMove.ToSquare),
			bestMovePromotion:  bestMove.Promotion,
			epoch:              uint8(worker.engine.currentEpoch.Load()),
			nodeType:           nType,
		}
		worker.engine.setHash(hash)
	}

	return value
}

func (worker *workerThread) minimize(ctx context.Context, legalMoves []chess.Move, pos *chess.Position,
	a int16, b int16, depth uint8, currentZobr uint64) int16 {
	newPos := worker.engine.positionPool.Get().(*chess.Position)
	defer worker.engine.positionPool.Put(newPos)
	value := int16(math.MaxInt16)
	oldA := a
	oldB := b
	bestMove := chess.Move{}

	for _, move := range legalMoves {
		*newPos = *pos
		newZobr := updateHash(currentZobr, newPos, move) // UpdateHash performs move.
		newPosScore := worker.alphaBetaSearch(ctx, newPos, true, a, b, depth-1, newZobr)

		if worker.stop {
			break
		}

		if newPosScore < value {
			value = newPosScore
			bestMove = move
		}

		b = min(b, value)
		if value <= a {
			break
		}
	}

	if !worker.stop {
		// Determine Node Type
		var nType uint8
		if value <= oldA {
			nType = nodeAlphaBound // Upper Bound
		} else if value >= oldB {
			nType = nodeBetaBound // Lower Bound (rare in minimize, common in maximize)
		} else {
			nType = nodeExact
		}

		// Store in TT with best move and node type
		hash := hashEntry{
			hash:               currentZobr,
			score:              value,
			depth:              depth,
			bestMoveFromSquare: compressSquare(bestMove.FromSquare),
			bestMoveToSquare:   compressSquare(bestMove.ToSquare),
			bestMovePromotion:  bestMove.Promotion,
			epoch:              uint8(worker.engine.currentEpoch.Load()),
			nodeType:           nType,
		}
		worker.engine.setHash(hash)
	}

	return value
}

func handleTerminalNode(pos *chess.Position, depth uint8) int16 {
	if pos.IsCheck() {
		switch pos.SideToMove {
		case chess.Black:
			// white wins
			return 32000 + int16(depth)
		case chess.White:
			// black wins
			return -32000 - int16(depth)
		}
	}
	// its a draw
	return 0
}

func (worker *workerThread) getPrimaryVariation(position *chess.Position, depth uint8) []chess.Move {
	pv := make([]chess.Move, 0, depth)

	currentHash := calculateHash(position)

	for i := uint8(0); i < depth; i++ {
		hashEntry, ok := worker.engine.getHash(currentHash)
		move := hashEntry.bestMove()

		// 1. Stop if no entry or move is empty
		if !ok || move == (chess.Move{}) {
			break
		}

		pv = append(pv, move)

		// 3. Update BOTH the hash and the board state
		// This ensures the next iteration's updateHash sees the correct pieces
		currentHash = updateHash(currentHash, position, move)
	}
	return pv
}

func (worker *workerThread) quiescenceSearch(ctx context.Context, pos *chess.Position, maximizingPlayer bool, a int16, b int16) int16 {
	worker.nodesSearched++
	if worker.nodesSearched&0x7FF == 0 && ctx.Err() != nil {
		worker.stop = true
	}
	if worker.stop {
		return 0
	}

	// 1. The "Stand Pat" score: what if we just don't capture anything?
	standPat := scorePosition(pos)

	// 2. Evaluate Stand Pat bounds
	if maximizingPlayer {
		if standPat >= b {
			return b // Beta cutoff: we're already too good, opponent will avoid this
		}
		a = max(a, standPat)
	} else {
		if standPat <= a {
			return a // Alpha cutoff
		}
		b = min(b, standPat)
	}

	// 3. Generate ONLY noisy moves (Captures)
	legalMoves := chess.LegalMoves(pos)
	var captures []chess.Move

	for _, move := range legalMoves {
		isCapture := pos.Piece(move.ToSquare) != chess.NoPiece || move.ToSquare == pos.EnPassant
		if isCapture {
			captures = append(captures, move)
		}
	}

	// If there are no captures, the position is "quiet". Return the static eval.
	if len(captures) == 0 {
		return standPat
	}

	// --- NEW: MVV-LVA SORTING ---
	// Sort the captures slice so the highest MVV-LVA scores are at index 0.
	sort.Slice(captures, func(i, j int) bool {
		return scoreMVVLVA(pos, captures[i]) > scoreMVVLVA(pos, captures[j])
	})
	// ----------------------------

	// 4. Search the captures
	if maximizingPlayer {
		return worker.qMaximize(ctx, captures, pos, a, b, standPat)
	} else {
		return worker.qMinimize(ctx, captures, pos, a, b, standPat)
	}
}

func (worker *workerThread) qMaximize(ctx context.Context, captures []chess.Move, pos *chess.Position, a int16, b int16, standPat int16) int16 {
	newPos := worker.engine.positionPool.Get().(*chess.Position)
	defer worker.engine.positionPool.Put(newPos)

	value := standPat

	for _, move := range captures {
		*newPos = *pos
		newPos.Move(move)
		// Notice: No updateHash() here. QS trees are very deep and narrow;
		// the overhead of TT lookups in QS usually outweighs the benefits.

		score := worker.quiescenceSearch(ctx, newPos, false, a, b)

		if worker.stop {
			break
		}

		value = max(value, score)
		a = max(a, value)
		if value >= b {
			break // Beta cutoff
		}
	}

	return value
}

func (worker *workerThread) qMinimize(ctx context.Context, captures []chess.Move, pos *chess.Position, a int16, b int16, standPat int16) int16 {
	newPos := worker.engine.positionPool.Get().(*chess.Position)
	defer worker.engine.positionPool.Put(newPos)

	value := standPat

	for _, move := range captures {
		*newPos = *pos
		newPos.Move(move)

		score := worker.quiescenceSearch(ctx, newPos, true, a, b)

		if worker.stop {
			break
		}

		value = min(value, score)
		b = min(b, value)
		if value <= a {
			break // Alpha cutoff
		}
	}

	return value
}

///////////////////////////////////////////////////////////////////////////////
// Position Scoring ///////////////////////////////////////////////////////////
///////////////////////////////////////////////////////////////////////////////

// Piece Square Tables for rewarding pieces on certain squares.
var pawnPST = [64]int16{
	+00, +00, +00, +00, +00, +00, +00, +00,
	+50, +50, +50, +50, +50, +50, +50, +50,
	+10, +10, +20, +30, +30, +20, +10, +10,
	+05, +05, +15, +25, +25, +15, +05, +05,
	+00, +00, +15, +21, +20, +15, +00, +00,
	+05, -05, +10, +00, +00, +10, -05, +05,
	+05, +10, +00, -20, -20, +00, +10, +05,
	+00, +00, +00, +00, +00, +00, +00, +00,
}
var knightPST = [64]int16{
	-25, -15, -10, -10, -10, -10, -15, -25,
	-15, -05, +00, +05, +05, +00, -05, -15,
	-10, +05, +10, +15, +15, +10, +05, -10,
	-10, +00, +15, +20, +20, +15, +00, -10,
	-10, +05, +15, +20, +20, +15, +05, -10,
	-10, +00, +10, +15, +15, +10, +00, -10,
	-15, -05, +00, +00, +00, +00, -05, -15,
	-25, -15, -10, -10, -10, -10, -15, -25,
}
var bishopPST = [64]int16{
	-20, -10, -10, -10, -10, -10, -10, -20,
	-10, +00, +00, +00, +00, +00, +00, -10,
	-10, +00, +05, +10, +10, +05, +00, -10,
	-10, +05, +05, +10, +10, +05, +05, -10,
	-10, +00, +10, +10, +10, +10, +00, -10,
	-10, +10, +10, +10, +10, +10, +10, -10,
	-10, +15, +00, +00, +00, +00, +15, -10,
	-20, -10, -10, -10, -10, -10, -10, -20,
}
var rookPST = [64]int16{
	+00, +00, +00, +05, +05, +00, +00, +00,
	+05, +10, +10, +10, +10, +10, +10, +05,
	-05, +00, +00, +00, +00, +00, +00, -05,
	-05, +00, +00, +00, +00, +00, +00, -05,
	-05, +00, +00, +00, +00, +00, +00, -05,
	-05, +00, +00, +00, +00, +00, +00, -05,
	-05, +00, +00, +00, +00, +00, +00, -05,
	+00, +00, +00, +05, +05, +00, +00, +00,
}
var queenPST = [64]int16{
	-20, -10, -10, -05, -05, -10, -10, -20,
	-10, +00, +00, +00, +00, +00, +00, -10,
	-10, +00, +05, +05, +05, +05, +00, -10,
	-05, +00, +05, +05, +05, +05, +00, -05,
	+00, +00, +05, +05, +05, +05, +00, -05,
	-10, +05, +05, +05, +05, +05, +00, -10,
	-10, +00, +05, +00, +00, +00, +00, -10,
	-20, -10, -10, -05, -05, -10, -10, -20,
}
var kingPST = [64]int16{
	-30, -40, -40, -50, -50, -40, -40, -30,
	-30, -40, -40, -50, -50, -40, -40, -30,
	-30, -40, -40, -50, -50, -40, -40, -30,
	-30, -40, -40, -50, -50, -40, -40, -30,
	-20, -30, -30, -40, -40, -30, -30, -20,
	-10, -20, -20, -20, -20, -20, -20, -10,
	+20, +20, +00, +00, +00, +00, +20, +20,
	+20, +30, +10, +00, +00, +10, +30, +20,
}

func scorePosition(pos *chess.Position) int16 {
	var score int16

	// Piece values consistent with your previous constants
	const (
		pawnValue   = 100
		knightValue = 275
		bishopValue = 300
		rookValue   = 500
		queenValue  = 900
		kingValue   = 0 // King material doesn't change
	)

	// 1. Evaluate White Pieces
	score += evaluatePieceType(pos.Bitboard(chess.WhitePawn), pawnValue, pawnPST, false)
	score += evaluatePieceType(pos.Bitboard(chess.WhiteKnight), knightValue, knightPST, false)
	score += evaluatePieceType(pos.Bitboard(chess.WhiteBishop), bishopValue, bishopPST, false)
	score += evaluatePieceType(pos.Bitboard(chess.WhiteRook), rookValue, rookPST, false)
	score += evaluatePieceType(pos.Bitboard(chess.WhiteQueen), queenValue, queenPST, false)
	score += evaluatePieceType(pos.Bitboard(chess.WhiteKing), kingValue, kingPST, false)

	// 2. Evaluate Black Pieces (Using the 'flip' flag for PST perspective)
	score -= evaluatePieceType(pos.Bitboard(chess.BlackPawn), pawnValue, pawnPST, true)
	score -= evaluatePieceType(pos.Bitboard(chess.BlackKnight), knightValue, knightPST, true)
	score -= evaluatePieceType(pos.Bitboard(chess.BlackBishop), bishopValue, bishopPST, true)
	score -= evaluatePieceType(pos.Bitboard(chess.BlackRook), rookValue, rookPST, true)
	score -= evaluatePieceType(pos.Bitboard(chess.BlackQueen), queenValue, queenPST, true)
	score -= evaluatePieceType(pos.Bitboard(chess.BlackKing), kingValue, kingPST, true)

	return score
}

func evaluatePieceType(bb chess.Bitboard, val int16, pst [64]int16, isBlack bool) int16 {
	var total int16
	mask := uint64(bb)

	for mask != 0 {
		// Get index of the set bit (0-63)
		sq := bits.TrailingZeros64(mask)
		total += val

		pstIdx := sq
		if !isBlack {
			// Flips the square for Black (mirrors the board)
			// e.g., square 0 (a1) becomes 56 (a8)
			pstIdx = sq ^ 56
		}
		total += pst[pstIdx]

		// Bitwise trick to clear the least significant bit
		mask &= mask - 1
	}
	return total
}

// Helper to get a rough sorting value for pieces
func getPieceSortValue(p chess.PieceType) int {
	switch p {
	case chess.Pawn:
		return 100
	case chess.Knight:
		return 275
	case chess.Bishop:
		return 300
	case chess.Rook:
		return 500
	case chess.Queen:
		return 900
	case chess.King:
		return 10000 // Just for sorting, though capturing the King is rare
	default:
		return 0
	}
}

// scoreMVVLVA returns an integer representing the priority of a capture.
func scoreMVVLVA(pos *chess.Position, move chess.Move) int {
	attacker := pos.Piece(move.FromSquare)
	victim := pos.Piece(move.ToSquare)

	victimVal := getPieceSortValue(victim.Type)

	// Handle En Passant (the ToSquare is empty, but the victim is a pawn)
	if move.ToSquare == pos.EnPassant && attacker.Type == chess.Pawn {
		victimVal = 100 // It's a Pawn
	}

	attackerVal := getPieceSortValue(attacker.Type)

	// Primary sort: Victim Value. Secondary sort: Attacker Value (inverted)
	return (victimVal * 100) - attackerVal
}

func (engine *engine) calculateTimeLimits(cmd *uci.EvaluateCmd) (time.Duration, time.Duration) {
	// 1. Handle fixed limits (Infinite, Ponder)
	if cmd.Infinite {
		return time.Hour * 96, time.Hour * 96
	}

	// 2. Handle exact move time (e.g., "go movetime 1000")
	if cmd.MoveTime.HasValue() {
		exactTime := time.Duration(cmd.MoveTime.Value()) * time.Millisecond
		return exactTime, exactTime
	}

	// 3. Dynamic Time Control
	var timeLeft, inc int
	if engine.position.SideToMove == chess.White {
		if cmd.Wtime.HasValue() {
			timeLeft = cmd.Wtime.Value()
		}
		if cmd.Winc.HasValue() {
			inc = cmd.Winc.Value()
		}
	} else {
		if cmd.Btime.HasValue() {
			timeLeft = cmd.Btime.Value()
		}
		if cmd.Binc.HasValue() {
			inc = cmd.Binc.Value()
		}
	}

	// If no time is provided, default to a safe infinite search
	if timeLeft <= 0 {
		return time.Hour * 24, time.Hour * 24
	}

	// Calculate how many moves we expect are left
	movesToGo := 40 // Default assumption for Sudden Death
	if cmd.MovesToGo.HasValue() && cmd.MovesToGo.Value() > 0 {
		movesToGo = cmd.MovesToGo.Value()
	}

	// Baseline allocation: fraction of remaining time + a piece of the increment
	timeAllocation := (timeLeft / movesToGo) + (inc / 2)

	// Safety check: Never allocate more time than we actually have!
	if timeAllocation >= timeLeft {
		timeAllocation = timeLeft - 50 // Leave a 50ms network/overhead buffer
	}
	if timeAllocation < 20 {
		timeAllocation = 20 // Minimum 20 so we don't crash the search
	}

	softLimit := time.Duration(timeAllocation) * time.Millisecond

	// Hard limit gives the engine a bit of leeway to finish the current depth,
	// but strictly cuts off before we flag.
	hardLimit := time.Duration(min(timeAllocation*2, timeLeft-50)) * time.Millisecond

	return softLimit, hardLimit
}
