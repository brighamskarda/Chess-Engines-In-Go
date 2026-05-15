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
	"math/rand/v2"

	"github.com/brighamskarda/chess/v2"
)

const (
	nodeExact      uint8 = iota // Score is precise
	nodeAlphaBound              // Fail-low: score is at most this much (Upper Bound)
	nodeBetaBound               // Fail-high: score is at least this much (Lower Bound)
)

type hashEntry struct {
	// hash, Zobrist hash, collisions are very unlikely
	hash uint64
	// score in centipawns, negative for black.
	score int16
	// depth searched
	depth uint8

	bestMoveFromSquare hashSquare
	bestMoveToSquare   hashSquare
	bestMovePromotion  chess.PieceType

	// epoch helps the engine know how old this hash entry is.
	epoch    uint8
	nodeType uint8
}

func (entry hashEntry) bestMove() chess.Move {
	return chess.Move{
		FromSquare: expandSquare(entry.bestMoveFromSquare),
		ToSquare:   expandSquare(entry.bestMoveToSquare),
		Promotion:  entry.bestMovePromotion,
	}
}

// hashSquare is a condensed square used for smaller hash tables entries.
type hashSquare uint8

func compressSquare(square chess.Square) hashSquare {
	return hashSquare(uint8((square.Rank-1)*8) + uint8(square.File-1))
}

func expandSquare(compressed hashSquare) chess.Square {
	return chess.Square{
		File: chess.File(compressed%8 + 1),
		Rank: chess.Rank(compressed/8 + 1),
	}
}

///////////////////////////////////////////////////////////////////////////////
// Zobrist Hashing ////////////////////////////////////////////////////////////
///////////////////////////////////////////////////////////////////////////////

// Constants for indexing
const (
	zobrWhitePawn = iota
	zobrWhiteKnight
	zobrWhiteBishop
	zobrWhiteRook
	zobrWhiteQueen
	zobrWhiteKing
	zobrBlackPawn
	zobrBlackKnight
	zobrBlackBishop
	zobrBlackRook
	zobrBlackQueen
	zobrBlackKing
)

var (
	zobrPieceKeys    [12][64]uint64
	zobrSideKey      uint64
	zobrCastlingKeys [16]uint64
	zobrEPKeys       [8]uint64
)

// InitZobrist populates the table with random 64-bit integers
func init() {
	// Use a fixed seed if you want consistent hashes across runs
	source := rand.NewPCG(67, 420)
	r := rand.New(source)

	for p := 0; p < 12; p++ {
		for s := 0; s < 64; s++ {
			zobrPieceKeys[p][s] = r.Uint64()
		}
	}

	zobrSideKey = r.Uint64()

	for i := 0; i < 16; i++ {
		zobrCastlingKeys[i] = r.Uint64()
	}

	for i := 0; i < 8; i++ {
		zobrEPKeys[i] = r.Uint64()
	}
}

func getZobrNum(p chess.Piece) uint64 {
	switch p {
	case chess.WhitePawn:
		return zobrWhitePawn
	case chess.WhiteKnight:
		return zobrWhiteKnight
	case chess.WhiteBishop:
		return zobrWhiteBishop
	case chess.WhiteRook:
		return zobrWhiteRook
	case chess.WhiteQueen:
		return zobrWhiteQueen
	case chess.WhiteKing:
		return zobrWhiteKing
	case chess.BlackPawn:
		return zobrBlackPawn
	case chess.BlackKnight:
		return zobrBlackKnight
	case chess.BlackBishop:
		return zobrBlackBishop
	case chess.BlackRook:
		return zobrBlackRook
	case chess.BlackQueen:
		return zobrBlackQueen
	case chess.BlackKing:
		return zobrBlackKing
	default:
		panic("Invalid piece for zobrist number")
	}
}

// CalculateHash generates the hash from scratch
func calculateHash(pos *chess.Position) uint64 {
	var h uint64

	for _, square := range chess.AllSquares {
		piece := pos.Piece(square)
		if piece == chess.NoPiece {
			continue
		}
		h ^= zobrPieceKeys[getZobrNum(piece)][compressSquare(square)]
	}

	if pos.SideToMove == chess.Black { // Black to move
		h ^= zobrSideKey
	}

	castlingIdx := getCastlingIndex(pos)
	h ^= zobrCastlingKeys[castlingIdx]

	if pos.EnPassant != chess.NoSquare {
		h ^= zobrEPKeys[pos.EnPassant.File-1]
	}

	return h
}

func getCastlingIndex(pos *chess.Position) int {
	index := 0
	if pos.WhiteKsCastle {
		index |= 1
	}
	if pos.WhiteQsCastle {
		index |= 2
	}
	if pos.BlackKsCastle {
		index |= 4
	}
	if pos.BlackQsCastle {
		index |= 8
	}
	return index
}

func updateHash(currentHash uint64, pos *chess.Position, move chess.Move) uint64 {
	h := currentHash

	// 1. XOR out the moving piece from its origin
	movingPiece := pos.Piece(move.FromSquare)
	pIdx := getZobrNum(movingPiece)
	h ^= zobrPieceKeys[pIdx][compressSquare(move.FromSquare)]

	// 2. If it's a capture, XOR out the piece being removed
	capturedPiece := pos.Piece(move.ToSquare)
	if capturedPiece != chess.NoPiece {
		capIdx := getZobrNum(capturedPiece)
		h ^= zobrPieceKeys[capIdx][compressSquare(move.ToSquare)]
	}

	// 3. XOR in the piece at its new destination
	// If it's a promotion, use the promotion piece instead of the moving piece
	if move.Promotion != chess.NoPieceType {
		promIdx := getZobrNum(chess.Piece{
			Color: movingPiece.Color,
			Type:  move.Promotion,
		})
		h ^= zobrPieceKeys[promIdx][compressSquare(move.ToSquare)]
	} else {
		h ^= zobrPieceKeys[pIdx][compressSquare(move.ToSquare)]
	}

	// 4. Handle Special Cases: En Passant capture
	// In EP, the captured pawn isn't on move.To, so we XOR it out manually
	if pos.EnPassant == move.ToSquare && movingPiece.Type == chess.Pawn {
		var epPawnSquare chess.Square
		if pos.SideToMove == chess.White {
			epPawnSquare = chess.Square{
				File: move.ToSquare.File,
				Rank: move.ToSquare.Rank - 1,
			}
			h ^= zobrPieceKeys[zobrBlackPawn][compressSquare(epPawnSquare)]
		} else {
			epPawnSquare = chess.Square{
				File: move.ToSquare.File,
				Rank: move.ToSquare.Rank + 1,
			}
			h ^= zobrPieceKeys[zobrWhitePawn][compressSquare(epPawnSquare)]
		}
	}

	// 5. Handle Special Cases: Castling
	// Move the Rook manually (the King is already handled by steps 1 & 3)
	if isCastleMove(pos, move) {
		switch move.ToSquare {
		case chess.G1: // White King-side
			h ^= zobrPieceKeys[zobrWhiteRook][compressSquare(chess.H1)] // Out
			h ^= zobrPieceKeys[zobrWhiteRook][compressSquare(chess.F1)] // In
		case chess.C1: // White King-side
			h ^= zobrPieceKeys[zobrWhiteRook][compressSquare(chess.A1)] // Out
			h ^= zobrPieceKeys[zobrWhiteRook][compressSquare(chess.D1)] // In
		case chess.G8: // White King-side
			h ^= zobrPieceKeys[zobrBlackRook][compressSquare(chess.H8)] // Out
			h ^= zobrPieceKeys[zobrBlackRook][compressSquare(chess.F8)] // In
		case chess.C8: // White King-side
			h ^= zobrPieceKeys[zobrBlackRook][compressSquare(chess.A8)] // Out
			h ^= zobrPieceKeys[zobrBlackRook][compressSquare(chess.D8)] // In
		}
	}

	// 6. XOR out OLD Castling and EP rights
	h ^= zobrCastlingKeys[getCastlingIndex(pos)]
	if pos.EnPassant != chess.NoSquare {
		h ^= zobrEPKeys[pos.EnPassant.File-1]
	}

	pos.Move(move)

	// 7. XOR in NEW Castling and EP rights (after they change)
	// nextPos would be the state after the move is made
	h ^= zobrCastlingKeys[getCastlingIndex(pos)]
	if pos.EnPassant != chess.NoSquare {
		h ^= zobrEPKeys[pos.EnPassant.File-1]
	}

	// 8. Always flip the side to move
	h ^= zobrSideKey

	return h
}

func isCastleMove(pos *chess.Position, m chess.Move) bool {
	return (pos.SideToMove == chess.White && (m == (chess.Move{
		FromSquare: chess.E1,
		ToSquare:   chess.G1,
	}) && pos.WhiteKsCastle) || (m == (chess.Move{
		FromSquare: chess.E1,
		ToSquare:   chess.C1,
	}) && pos.WhiteQsCastle)) ||
		(pos.SideToMove == chess.Black && (m == (chess.Move{
			FromSquare: chess.E8,
			ToSquare:   chess.G8,
		}) && pos.BlackKsCastle) || (m == (chess.Move{
			FromSquare: chess.E8,
			ToSquare:   chess.C8,
		}) && pos.BlackQsCastle))
}
