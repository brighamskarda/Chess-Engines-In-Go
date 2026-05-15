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
	"log/slog"
	"math"
	"math/bits"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/brighamskarda/chess/v2"
	"github.com/brighamskarda/chess/v2/uci"
)

const infoChannelSize = 128

// defaultHashSize is the default size of the hash table in megabytes.
const defaultHashSize = 64
const minHashSize = 1
const maxHashSize = 32768

const defaultThreads = 1
const minThreads = 1
const maxThreads = 1024

const defaultMultiPv = 1
const minMultiPv = 1
const maxMultiPv = 256

const maxDepth = math.MaxUint8 - 1

type engine struct {
	// position is the current position to evaluate.
	position *chess.Position
	// infoCallback is stored so it can be used in situations where you wish to wait on it.
	infoCallback func(*uci.InfoCmd)

	// hashTable is a hash of precomputed values.
	hashTable []hashEntry
	// hashMask is used for fast retrieval of entries from hashTable.
	hashMask uint64
	// currentEpoch increments every time evaluate is called.
	currentEpoch atomic.Int32
	// debug indicates if more messages should be sent.
	debug atomic.Bool

	// infoPool reduces the garbage collection associated with
	// constantly creating *uci.InfoCmd objects.
	infoPool sync.Pool
	// infoChan accepts infos that should be sent to the client.
	infoChan chan<- *uci.InfoCmd

	// evalResponsePool is a pool of channels used to respond to evaluation requests.
	evalResponsePool sync.Pool
	// evalRequestChan is used to request evaluations.
	//
	// It is closed and reset whenever the number of worker threads changes
	// so that existing worker threads are stopped.
	evalRequestChan chan<- evalRequest
	// evalContext cancels an ongoing evaluation
	evalContextCancel context.CancelFunc
	// evalContextLock ensures that Stop() can safely access evalContext.
	evalContextLock sync.Mutex

	// positionsPool helps reduce garbage collection from the alphabeta search function.
	positionPool sync.Pool

	// ctx indicates when the engine should shut down.
	ctx context.Context
	// cancel the engine's context so it shuts down.
	cancel context.CancelFunc
	// waitGroup indicates when all processes have shutdown and Quit can return.
	waitGroup sync.WaitGroup

	// logger is used to output info messages that couldn't be sent before the engine shut down.
	logger slog.Logger

	// --- Pondering Fields ---
	ponderMutex   sync.Mutex
	isPondering   bool
	ponderHitChan chan struct{}
	clockStart    time.Time

	// How many primary variations to send out.
	multiPV atomic.Int32
}

func (engine *engine) Initialize(infoCallback func(*uci.InfoCmd)) {
	engine.ctx, engine.cancel = context.WithCancel(context.Background())

	engine.infoPool = sync.Pool{
		New: func() any {
			return &uci.InfoCmd{}
		},
	}

	engine.evalResponsePool = sync.Pool{
		New: func() any {
			return make(chan evalResponse)
		},
	}

	engine.positionPool = sync.Pool{
		New: func() any {
			return &chess.Position{}
		},
	}

	engine.infoCallback = infoCallback
	engine.multiPV.Store(1)

	infoChan := make(chan *uci.InfoCmd, infoChannelSize)
	engine.infoChan = infoChan
	engine.waitGroup.Go(func() {
		engine.infoSender(infoCallback, infoChan)
	})

	engine.resetHashTable(defaultHashSize)
	engine.resetWorkerThreads(defaultThreads)
}

func (engine *engine) CopyProtection() bool {
	return true
}

func (engine *engine) Register(ignore *uci.RegisterCmd) bool {
	return true
}

func (engine *engine) Name() string {
	return "Apple Gopher V0.1"
}

func (engine *engine) Author() string {
	return "Brigham Skarda"
}

func (engine *engine) Options() []uci.Option {
	return []uci.Option{
		&uci.SpinOption{
			Name:         "Hash",
			DefaultValue: defaultHashSize,
			Min:          minHashSize,
			Max:          maxHashSize,
		},
		&uci.SpinOption{
			Name:         "Threads",
			DefaultValue: defaultThreads,
			Min:          minThreads,
			Max:          maxThreads,
		},
		&uci.CheckOption{
			Name:         "Ponder",
			DefaultValue: false,
		}, &uci.SpinOption{
			Name:         "MultiPV",
			DefaultValue: defaultMultiPv,
			Min:          minMultiPv,
			Max:          maxMultiPv,
		},
	}
}

func (engine *engine) SetDebug(val bool) {
	engine.debug.Store(val)
}

func (engine *engine) SetOption(option uci.SetOption) {
	switch o := option.(type) {
	case *uci.SetSpinOption:
		if o.OptionName() == "Hash" {
			if o.Value > maxHashSize || o.Value < minHashSize {
				engine.sendStringMsg(fmt.Sprintf("Ignoring invalid hash size %d", o.Value))
			}

			engine.resetHashTable(uint(o.Value))
		} else if o.OptionName() == "Threads" {
			if o.Value > maxThreads || o.Value < minThreads {
				engine.sendStringMsg(fmt.Sprintf("Ignoring invalid number of threads %d", o.Value))
			}

			engine.resetWorkerThreads(uint(o.Value))
		} else if o.OptionName() == "MultiPV" {
			if o.Value > maxMultiPv || o.Value < minMultiPv {
				engine.sendStringMsg(fmt.Sprintf("Ignoring invalid MultiPV %d", o.Value))
			}

			engine.multiPV.Store(int32(o.Value))
		}
	case *uci.SetCheckOption:
		if o.OptionName() == "Ponder" {
			if engine.debug.Load() {
				engine.sendStringMsg(fmt.Sprintf("Ponder set to %v", o.Checkbox))
			}

		}
	}
}

func (engine *engine) NewGame() {
	if engine.debug.Load() {
		engine.sendStringMsg("Resetting hash table for new game")
	}
	clear(engine.hashTable)
}

func (engine *engine) SetPosition(pos *chess.Position, moves []chess.Move) {
	if engine.debug.Load() {
		posText, _ := pos.MarshalText()
		movesString := strings.Builder{}
		for i, m := range moves {
			if i != 0 {
				movesString.WriteString(", ")
			}
			movesString.WriteString(m.String())
		}
		engine.sendStringMsg(fmt.Sprintf("Setting position %s: %s", posText, movesString.String()))
	}

	engine.position = pos

	for _, m := range moves {
		engine.position.Move(m)
	}
}

func (engine *engine) Evaluate(cmd *uci.EvaluateCmd) uci.BestMove {
	engine.waitGroup.Add(1)
	defer engine.waitGroup.Done()

	softLimit, hardLimit := engine.calculateTimeLimits(cmd)
	maxSearchDepth := uint8(maxDepth)
	if cmd.Depth.HasValue() {
		maxSearchDepth = uint8(cmd.Depth.Value())
	}

	// --- 1. Set up Pondering State ---
	engine.ponderMutex.Lock()
	engine.isPondering = cmd.Ponder
	engine.ponderHitChan = make(chan struct{})
	engine.clockStart = time.Now()
	engine.ponderMutex.Unlock()

	// Use WithCancel instead of WithTimeout so we can control exactly when the hard limit starts
	evalContext, cancel := context.WithCancel(engine.ctx)
	engine.setEvalContextCancel(cancel)
	defer cancel()

	// --- 2. Dynamic Hard Limit Goroutine ---
	go func() {
		if cmd.Ponder {
			// Wait for a ponderhit or a stop command
			select {
			case <-engine.ponderHitChan:
				// Ponderhit received! The clock has started.
			case <-evalContext.Done():
				return // Engine was stopped, exit the routine
			}
		}

		// Calculate how much hard limit time we have left since the clock officially started
		engine.ponderMutex.Lock()
		start := engine.clockStart
		engine.ponderMutex.Unlock()

		timeToHardLimit := hardLimit - time.Since(start)
		if timeToHardLimit > 0 {
			select {
			case <-time.After(timeToHardLimit):
				cancel() // Kill the search
			case <-evalContext.Done():
			}
		} else {
			cancel() // We are already out of time
		}
	}()

	engine.debugEvalStart()
	engine.currentEpoch.Add(1)

	legalMoves := chess.LegalMoves(engine.position)
	if len(legalMoves) == 0 {
		return uci.BestMove{}
	}

	// startTime tracks the absolute start of the search for accurate Nodes-Per-Second stats
	startTime := time.Now()
	var finalBestMove chess.Move
	var finalBestResponse evalResponse
	var totalNodes int

	// --- ITERATIVE DEEPENING LOOP ---
	// We search depth 1, then 2, then 3...
	// This sequential approach is what makes the Transposition Table useful.
	for d := uint8(1); d <= maxSearchDepth; d++ {

		// Create a channel for this specific depth's results
		// We want one response per legal move
		results := make([]evalResponse, len(legalMoves))
		responseChans := make([]chan evalResponse, len(legalMoves))

		for i, move := range legalMoves {
			responseChans[i] = engine.evalResponsePool.Get().(chan evalResponse)

			// Setup the position after this specific root move
			pos := engine.positionPool.Get().(*chess.Position)
			*pos = *engine.position
			pos.Move(move)

			// Send request to workers.
			// Note: We search d-1 because we already made 1 move at the root.
			engine.evalRequestChan <- evalRequest{
				position: pos,
				response: responseChans[i],
				depth:    d - 1,
				ctx:      evalContext,
			}
		}

		// Gather responses for THIS depth
		for i, ch := range responseChans {
			results[i] = <-ch
			engine.evalResponsePool.Put(ch)
		}

		// Stop if the context was cancelled (out of time)
		if evalContext.Err() != nil {
			if engine.debug.Load() {
				engine.sendStringMsg(fmt.Sprintf("Search aborted at depth %d due to hard limit or stop command.", d))
			}
			break
		}

		type pvLine struct {
			move chess.Move
			res  evalResponse
		}
		lines := make([]pvLine, len(legalMoves))
		for i := range legalMoves {
			lines[i] = pvLine{move: legalMoves[i], res: results[i]}
		}

		// 3. SORT BY SCORE (White wants high, Black wants low)
		sort.Slice(lines, func(i, j int) bool {
			if engine.position.SideToMove == chess.White {
				return lines[i].res.score > lines[j].res.score
			}
			return lines[i].res.score < lines[j].res.score
		})

		// 4. REPORT TOP N LINES
		for i := 0; i < int(engine.multiPV.Load()) && i < len(lines); i++ {
			currentLine := lines[i]
			pv := append([]chess.Move{currentLine.move}, currentLine.res.primaryVariation...)

			// Send info with the 'multipv' rank
			engine.sendMultiPVInfo(int(d), totalNodes, startTime, currentLine.res.score, pv, i+1)
		}

		// --- FIND BEST MOVE FOR THIS DEPTH ---
		bestIdx := 0
		for i := 1; i < len(results); i++ {
			if engine.position.SideToMove == chess.White {
				if results[i].score > results[bestIdx].score {
					bestIdx = i
				}
			} else {
				if results[i].score < results[bestIdx].score {
					bestIdx = i
				}
			}
		}

		// Update our "Best Found So Far"
		finalBestMove = legalMoves[bestIdx]
		finalBestResponse = results[bestIdx]

		// --- REPORT PROGRESS TO UCI ---
		// As we finish each depth, we report the PV and score.
		for _, r := range results {
			totalNodes += int(r.nodes)
		}

		pv := append([]chess.Move{finalBestMove}, finalBestResponse.primaryVariation...)

		// Send info to the GUI (Note: We use startTime so the GUI sees the total ponder time)
		engine.sendIterationInfo(int(d), totalNodes, startTime, finalBestResponse.score, pv)

		// --- 3. SOFT LIMIT CHECK ---
		engine.ponderMutex.Lock()
		pondering := engine.isPondering
		clockTime := engine.clockStart
		engine.ponderMutex.Unlock()

		// If we are actively pondering, we never break for a soft limit
		if !pondering {
			elapsed := time.Since(clockTime)
			if elapsed >= softLimit {
				if engine.debug.Load() {
					engine.sendStringMsg(fmt.Sprintf("Soft limit reached (Used: %s, Limit: %s). Stopping.", elapsed, softLimit))
				}
				break
			}
		}
	}

	var ponderMove uci.Optional[chess.Move]
	if len(finalBestResponse.primaryVariation) >= 2 {
		ponderMove = uci.OptionalOf(finalBestResponse.primaryVariation[1])
	}

	return uci.BestMove{
		Move:       finalBestMove,
		PonderMove: ponderMove,
	}
}

// Helper to send info strings during the search
func (engine *engine) sendIterationInfo(depth int, nodes int, start time.Time, score int16, pv []chess.Move) {
	milliSeconds := time.Since(start).Milliseconds()
	if milliSeconds == 0 {
		milliSeconds = 1
	}

	msg := engine.infoPool.Get().(*uci.InfoCmd)
	msg.Depth = uci.OptionalOf(depth)
	msg.Nodes = uci.OptionalOf(nodes)
	msg.Nps = uci.OptionalOf(int(float64(nodes) / (float64(milliSeconds) / 1000.0)))
	msg.Pv = pv
	msg.Time = uci.OptionalOf(int(milliSeconds))
	// --- MATE DETECTION LOGIC ---
	infoScore := uci.InfoScore{}
	if score > 30000 {
		// Calculate plies from root, then convert to full moves
		plies := depth - (int(score) - 32000)
		infoScore.Score = (plies + 1) / 2
		infoScore.IsMate = true
	} else if score < -30000 {
		plies := depth - (int(-score) - 32000)
		infoScore.Score = -((plies + 1) / 2)
		infoScore.IsMate = true
	} else {
		infoScore.Score = int(score)
	}
	msg.Score = uci.OptionalOf(infoScore)
	engine.infoCallback(msg)
}

// sendMultiPVInfo sends info strings for specific PV ranks during MultiPV search.
func (engine *engine) sendMultiPVInfo(depth int, nodes int, start time.Time, score int16, pv []chess.Move, pvRank int) {
	milliSeconds := time.Since(start).Milliseconds()
	if milliSeconds == 0 {
		milliSeconds = 1
	}

	msg := engine.infoPool.Get().(*uci.InfoCmd)
	msg.Depth = uci.OptionalOf(depth)
	msg.Nodes = uci.OptionalOf(nodes)
	msg.Nps = uci.OptionalOf(int(float64(nodes) / (float64(milliSeconds) / 1000.0)))
	msg.Pv = pv
	msg.Time = uci.OptionalOf(int(milliSeconds))
	msg.Score = uci.OptionalOf(uci.InfoScore{Score: int(score)})

	// --- MATE DETECTION LOGIC ---
	infoScore := uci.InfoScore{}
	if score > 30000 {
		// Calculate plies from root, then convert to full moves
		plies := depth - (int(score) - 32000)
		infoScore.Score = (plies + 1) / 2
		infoScore.IsMate = true
	} else if score < -30000 {
		plies := depth - (int(-score) - 32000)
		infoScore.Score = -((plies + 1) / 2)
		infoScore.IsMate = true
	} else {
		infoScore.Score = int(score)
	}

	// Crucial: This tells the GUI which PV line (1, 2, 3...) this is.
	msg.MultiPv = uci.OptionalOf(pvRank)

	engine.infoCallback(msg)
}

func (engine *engine) Stop() {
	if engine.debug.Load() {
		engine.sendStringMsg("Requesting to stop evaluation")
	}
	engine.cancelEvalContext()
}

func (engine *engine) PonderHit() {
	engine.ponderMutex.Lock()
	defer engine.ponderMutex.Unlock()

	// Only trigger if we are actively pondering
	if engine.isPondering {
		if engine.debug.Load() {
			engine.sendStringMsgBlock("Ponder hit received! Starting the clock for time management.")
		}
		engine.isPondering = false
		engine.clockStart = time.Now()
		close(engine.ponderHitChan) // Unblock the timer goroutine
	}
}

func (engine *engine) Quit() {
	engine.cancel()

	engine.waitGroup.Wait()
	close(engine.infoChan)
	close(engine.evalRequestChan)
}

///////////////////////////////////////////////////////////////////////////////
// Start of Helper Functions //////////////////////////////////////////////////
///////////////////////////////////////////////////////////////////////////////

// infoSender sends info commands from the channel to the callback function until the context is cancelled.
func (engine *engine) infoSender(infoCallback func(*uci.InfoCmd), infos <-chan *uci.InfoCmd) {
Loop:
	for {
		select {
		case <-engine.ctx.Done():
			break Loop
		case nextInfo, ok := <-infos:
			if !ok {
				break Loop
			}
			infoCallback(nextInfo)
			// reset the info before putting it back into the pool.
			*nextInfo = uci.InfoCmd{}
			engine.infoPool.Put(nextInfo)
		}
	}

	// Flush out the info channel to prevent deadlocks
	go func() {
		for nextInfo := range infos {
			engine.logger.Info("could not send message before shutdown", slog.Any("*uci.InfoCmd", nextInfo))
		}
	}()
}

// resetHashTable sets the engine's hash table to be equal to or smaller than a size in MB.
func (engine *engine) resetHashTable(size uint) {
	if engine.debug.Load() {
		engine.sendStringMsg(fmt.Sprintf("Resetting hash table to %dMB", size))
	}

	sizeBytes := size * 1024 * 1024
	hashEntrySize := unsafe.Sizeof(hashEntry{})
	sliceSize := sizeBytes / uint(hashEntrySize)

	// Rounding down to the nearest power of two so hash retrieval is faster.
	sliceSize = 1 << (bits.Len64(uint64(sliceSize-1)) - 1)

	engine.hashTable = make([]hashEntry, sliceSize)
	engine.hashMask = uint64(sliceSize - 1)
}

// getHash returns a hashEntry from the engine's hash table.
//
// The key is verified to help prevent data race issues.
// If it is invalid the boolean is returned as false.
func (engine *engine) getHash(key uint64) (hashEntry, bool) {
	entry := engine.hashTable[key&engine.hashMask]
	if entry.hash == key {
		return entry, true
	}

	return hashEntry{}, false
}

// setHash sets a hash entry in the engine's hash table.
//
// By default the deeper entry is preferred
// (meaning the provided entry my be ignored).
// But old entries will be replaced with new entries
// even if they aren't very deep.
func (engine *engine) setHash(entry hashEntry) {
	const discardAge = 5

	existingEntry := engine.hashTable[entry.hash&engine.hashMask]
	ageDiff := entry.epoch - existingEntry.epoch
	if ageDiff > discardAge {
		engine.hashTable[entry.hash&engine.hashMask] = entry
	} else if entry.depth > existingEntry.depth {
		engine.hashTable[entry.hash&engine.hashMask] = entry
	}
}

// resetWorkerThreads creates the number of requested workers.
//
// Existing workers are discarded.
func (engine *engine) resetWorkerThreads(numWorkers uint) {
	if engine.debug.Load() {
		engine.sendStringMsg(fmt.Sprintf("Setting up %d worker threads", numWorkers))
	}

	if engine.evalRequestChan != nil {
		close(engine.evalRequestChan)
	}

	requestChan := make(chan evalRequest, 128)
	engine.evalRequestChan = requestChan
	for range numWorkers {
		worker := &workerThread{
			engine: engine,
		}
		go worker.start(requestChan)
	}
}

// sendStringMsg is a non-blocking helper function for the common request of sending a string message.
func (engine *engine) sendStringMsg(s string) {
	msg := engine.infoPool.Get().(*uci.InfoCmd)
	msg.StringMsg = uci.OptionalOf(s)
	engine.infoChan <- msg
}

// sendStringMsg is a blocking helper function for the common request of sending a string message.
func (engine *engine) sendStringMsgBlock(s string) {
	msg := engine.infoPool.Get().(*uci.InfoCmd)
	msg.StringMsg = uci.OptionalOf(s)
	engine.infoCallback(msg)
}

// debugEvalStart sends a message indicating that evaluation has started.
func (engine *engine) debugEvalStart() {
	if engine.debug.Load() {
		fen, _ := engine.position.MarshalText()
		engine.sendStringMsg(fmt.Sprintf("Starting evaluation on %s", fen))
	}
}

func (engine *engine) setEvalContextCancel(c context.CancelFunc) {
	engine.evalContextLock.Lock()
	engine.evalContextCancel = c
	engine.evalContextLock.Unlock()
}

func (engine *engine) cancelEvalContext() {
	engine.evalContextLock.Lock()
	if engine.evalContextCancel != nil {
		engine.evalContextCancel()
	}
	engine.evalContextLock.Unlock()
}
