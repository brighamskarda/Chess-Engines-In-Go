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
	"slices"
	"testing"

	"github.com/brighamskarda/chess/v2"
)

func TestCompressSquareValuesUnique(t *testing.T) {
	compressedSquares := make([]hashSquare, 0, 64)

	for _, square := range chess.AllSquares {
		compressed := compressSquare(square)

		if slices.Contains(compressedSquares, compressed) {
			t.Errorf("duplicate square found when compressed")
		} else {
			compressedSquares = append(compressedSquares, compressed)
		}
	}
}

func TestSquareExpansion(t *testing.T) {
	compressedSquares := make([]hashSquare, 0, 64)

	for _, square := range chess.AllSquares {
		compressed := compressSquare(square)
		compressedSquares = append(compressedSquares, compressed)
	}

	for i, compressed := range compressedSquares {
		square := expandSquare(compressed)
		if square != chess.AllSquares[i] {
			t.Errorf("expansion and decompression are not equivalent")
		}
	}
}
