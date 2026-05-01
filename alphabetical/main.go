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
	"bytes"
	"context"
	"os"

	"github.com/brighamskarda/chess/v2"
	"github.com/brighamskarda/chess/v2/uci"
)

// AlphabetEngine returns the first move alphabetically.
type AlphabetEngine struct {
	position *chess.Position
	// reverse is true to reverse the order of the alphabet.
	reverse bool
}

func (engine *AlphabetEngine) Initialize(ignore func(*uci.InfoCmd)) {
}

func (engine *AlphabetEngine) CopyProtection() bool {
	return true
}

func (engine *AlphabetEngine) Register(ignore *uci.RegisterCmd) bool {
	return true
}

func (engine *AlphabetEngine) Name() string {
	return "AlphabetEngine"
}

func (engine *AlphabetEngine) Author() string {
	return "Brigham Skarda"
}

func (engine *AlphabetEngine) Options() []uci.Option {
	return []uci.Option{
		&uci.CheckOption{
			Name:         "Reverse",
			DefaultValue: false,
		},
	}
}

func (engine *AlphabetEngine) SetDebug(ignore bool) {
}

func (engine *AlphabetEngine) SetOption(option uci.SetOption) {
	switch o := option.(type) {
	case *uci.SetCheckOption:
		if o.OptionName() == "Reverse" {
			engine.reverse = o.Checkbox
		}
	}
}

func (engine *AlphabetEngine) NewGame() {
}

func (engine *AlphabetEngine) SetPosition(pos *chess.Position, moves []chess.Move) {
	engine.position = pos

	for _, m := range moves {
		engine.position.Move(m)
	}
}

func (engine *AlphabetEngine) Evaluate(ignore *uci.EvaluateCmd) *uci.BestMove {
	legalMoves := chess.LegalMoves(engine.position)
	bestMove := legalMoves[0]
	bestMoveString, _ := bestMove.MarshalText()

	for _, m := range legalMoves {
		text, _ := m.MarshalText()
		if (!engine.reverse && bytes.Compare(text, bestMoveString) < 0) ||
			(engine.reverse && bytes.Compare(text, bestMoveString) > 0) {
			bestMove = m
			bestMoveString = text
		}
	}

	return &uci.BestMove{
		Move: bestMove,
	}
}

func (engine *AlphabetEngine) Stop() {
}

func (engine *AlphabetEngine) PonderHit() {
}

func (engine *AlphabetEngine) Quit() {
}

func main() {
	myEngine := &AlphabetEngine{}

	broker := uci.UciEngineBroker{
		Engine: myEngine,
		Input:  os.Stdin,
		Output: os.Stdout,
	}

	broker.Start(context.Background())
}
