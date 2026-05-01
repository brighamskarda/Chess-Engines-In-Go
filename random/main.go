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
	"math/rand/v2"
	"os"

	"github.com/brighamskarda/chess/v2"
	"github.com/brighamskarda/chess/v2/uci"
)

// RandomEngine plays random moves
type RandomEngine struct {
	position *chess.Position
}

func (engine *RandomEngine) Initialize(ignore func(*uci.InfoCmd)) {
}

func (engine *RandomEngine) CopyProtection() bool {
	return true
}

func (engine *RandomEngine) Register(ignore *uci.RegisterCmd) bool {
	return true
}

func (engine *RandomEngine) Name() string {
	return "Random Engine"
}

func (engine *RandomEngine) Author() string {
	return "Brigham Skarda"
}

func (engine *RandomEngine) Options() []uci.Option {
	return nil
}

func (engine *RandomEngine) SetDebug(ignore bool) {
}

func (engine *RandomEngine) SetOption(option uci.SetOption) {
}

func (engine *RandomEngine) NewGame() {
}

func (engine *RandomEngine) SetPosition(pos *chess.Position, moves []chess.Move) {
	engine.position = pos

	for _, m := range moves {
		engine.position.Move(m)
	}
}

func (engine *RandomEngine) Evaluate(ignore *uci.EvaluateCmd) *uci.BestMove {
	legalMoves := chess.LegalMoves(engine.position)
	return &uci.BestMove{
		Move: legalMoves[rand.Int()%len(legalMoves)],
	}
}

func (engine *RandomEngine) Stop() {
}

func (engine *RandomEngine) PonderHit() {
}

func (engine *RandomEngine) Quit() {
}

func main() {
	myEngine := &RandomEngine{}

	broker := uci.UciEngineBroker{
		Engine: myEngine,
		Input:  os.Stdin,
		Output: os.Stdout,
	}

	broker.Start(context.Background())
}
