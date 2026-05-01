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
	"os"

	"github.com/brighamskarda/chess/v2"
	"github.com/brighamskarda/chess/v2/uci"
)

// MinimalEngine will implement the simplest chess engine possible.
//
// It always just grabs the first legal move it sees.
type MinimalEngine struct {
	// We will store the position to evaluate here.
	position *chess.Position
}

func (engine *MinimalEngine) Initialize(ignore func(*uci.InfoCmd)) {
	// We don't need to initialize anything
	// and for simple engines it is okay to ignore the info command function.
}

func (engine *MinimalEngine) CopyProtection() bool {
	// Most engines won't use this, just return true.
	return true
}

func (engine *MinimalEngine) Register(ignore *uci.RegisterCmd) bool {
	// Most engines won't use this either, just return true.
	return true
}

func (engine *MinimalEngine) Name() string {
	// Give your engine a cool name.
	return "Minimal Engine"
}

func (engine *MinimalEngine) Author() string {
	// Don't forget to take credit for your hard work.
	return "Brigham Skarda"
}

func (engine *MinimalEngine) Options() []uci.Option {
	// No options are required by the UCI specification.
	return nil
}

func (engine *MinimalEngine) SetDebug(ignore bool) {
	// Out engine doesn't have a debug mode.
}

func (engine *MinimalEngine) SetOption(ignore uci.SetOption) {
	// Our engine doesn't support any options
}

func (engine *MinimalEngine) NewGame() {
	// New game isn't required for simple engines.
}

func (engine *MinimalEngine) SetPosition(pos *chess.Position, moves []chess.Move) {
	// We need to save the position we are being sent.
	engine.position = pos

	// We then need to perform the moves we were given on that position.
	for _, m := range moves {
		engine.position.Move(m)
	}

	// Some engines will store the move history
	// to see if they are entering a stalemate situation,
	// but this engine doesn't care.
}

func (engine *MinimalEngine) Evaluate(ignore *uci.EvaluateCmd) *uci.BestMove {
	// This is the brains of our engine.
	// For a super simple engine like this we don't need to worry about the options given in EvaluateCmd.

	// We will just return the first valid Move
	thisIsTheBest := chess.LegalMoves(engine.position)[0]
	return &uci.BestMove{
		Move: thisIsTheBest,
	}
}

func (engine *MinimalEngine) Stop() {
	// Our evaluation logic is so simple we never need to worry about ending it early.
}

func (engine *MinimalEngine) PonderHit() {
	// Our engine doesn't support pondering.
}

func (engine *MinimalEngine) Quit() {
	// We don't have any open files or long running searches we need to stop.
}

func main() {
	// We make our engine.
	myEngine := &MinimalEngine{}

	// We pass it into the broker.
	broker := uci.UciEngineBroker{
		Engine: myEngine,
		Input:  os.Stdin,
		Output: os.Stdout,
	}

	// broker.Start handles the rest.
	// context.Background(), is the default context for the go program.
	broker.Start(context.Background())

	// Voilà!!! You are now running a fully compliant UCI chess engine. Go ahead and try it.
	// I ran this engine in the Arena Chess, Lucas Chess, and En Croissant GUIs
	// and it worked flawlessly in all of them.
}
