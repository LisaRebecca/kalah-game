// Tournament Systems
//
// Copyright (c) 2022  Philip Kaludercic
//
// This file is part of go-kgp.
//
// go-kgp is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License,
// version 3, as published by the Free Software Foundation.
//
// go-kgp is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License, version 3, along with go-kgp. If not, see
// <http://www.gnu.org/licenses/>

package main

import (
	"fmt"
	"log"
	"sort"
)

// A tournament system decides what games to play, and records results
//
// All methods are called in a synchronised context, and do not have
// to be thread-safe.
type System interface {
	fmt.Stringer
	// Register a client as ready
	Ready(*Tournament, *Client)
	// Mark a client as dead
	Forget(*Tournament, *Client)
	// Record the outcome of a game
	Record(*Tournament, *Game)
	// Check if a tournament is over
	Over(*Tournament) bool
	// Called when the tournament is finished
	Deinit(*Tournament)
}

// roundRobin tournaments let every participant play with every other
// participant.
type roundRobin struct {
	// Board size for this tournament
	size uint
	// Set of games that we are expecting to play
	games map[*Game]struct{}
	// Set of clients that are ready to play a game
	ready []*Client
	// How many agents can pass on to the next round
	pick uint
	// How many games are we waiting on to finish
	active uint
}

// Generate a name for the current tournament
func (rr *roundRobin) String() string {
	return fmt.Sprintf("round-robin-%d", rr.size)
}

// Mark a client as ready and attempt to start a game
func (rr *roundRobin) Ready(t *Tournament, cli *Client) {
	if rr.games == nil {
		rr.games = make(map[*Game]struct{})
		for i, a := range t.participants {
			for j, b := range t.participants {
				if i <= j {
					continue
				}
				rr.games[&Game{
					Board: makeBoard(rr.size, rr.size),
					North: a,
					South: b,
				}] = struct{}{}
			}
		}
	}

	// Loop over all the ready clients to check if we still need
	// to organise a game between the new client and someone else.
	for i, ilc := range rr.ready {
		for game := range rr.games {
			b1 := game.North == cli && game.South == ilc
			b2 := game.North == ilc && game.South == cli

			// In case the new client and an existing
			// client both still have to play a game, we
			// will remove the waiting client from the
			// ready list and start the game
			if b1 || b2 {
				// Slice trick: "Delete without
				// preserving order (GC)"
				rr.ready[i] = rr.ready[len(rr.ready)-1]
				rr.ready[len(rr.ready)-1] = nil
				rr.ready = rr.ready[:len(rr.ready)-1]
				delete(rr.games, game)
				debug.Println(len(rr.games), "left in RR tournament")

				t.startGame(game)
				return
			}
		}
	}

	// If the client didn't find a match, mark it as ready and do
	// nothing more.
	rr.ready = append(rr.ready, cli)
}

// Remove all games that CLI should have participated in
func (rr *roundRobin) Forget(_ *Tournament, cli *Client) {
	for game := range rr.games {
		if game.North == cli || game.South == cli {
			delete(rr.games, game)
		}
	}
}

// The result has not to be recorded
func (*roundRobin) Record(*Tournament, *Game) {}

// Only allow the PICK best agents to proceed to a next round
func (rr *roundRobin) Deinit(t *Tournament) {
	// The last game just finished, sort the participants by score
	sort.SliceStable(t.participants, func(i, j int) bool {
		return t.participants[j].Score < t.participants[i].Score
	})
	// Find at least the top n agents
	n := int(rr.pick)
	if n > len(t.participants) {
		n = len(t.participants)
	}
	for n+1 < len(t.participants) && t.participants[n-1].Score == t.participants[n].Score {
		n++
	}
	// Forget the rest
	for i := 0; i < n; i++ {
		log.Printf("Passed: %s is on place %d (%f)",
			t.participants[i], i, t.participants[i].Score)
	}
	for i := n; i < len(t.participants); i++ {
		log.Printf("Eliminated: %s is on place %d (%f)",
			t.participants[i], i, t.participants[i].Score)
	}
	t.participants = t.participants[:n]
}

// A round robin tournament is over as soon as everyone has played a
// game against every other participant.  For n participants, this
// means every one has had n-1 games, ie. there have been n-1 rounds.
func (rr *roundRobin) Over(t *Tournament) bool {
	return rr.games != nil && len(rr.games) == 0
}

type random struct {
	done map[*Client]struct{}
	size uint
}

func (*random) String() string {
	return "rnd"
}

// Register a client as ready
func (rnd *random) Ready(t *Tournament, cli *Client) {
	if rnd.done == nil {
		rnd.done = make(map[*Client]struct{})
		rnd.done[nil] = struct{}{}
	}

	if _, done := rnd.done[cli]; done {
		return
	}

	t.startGame(&Game{
		Board: makeBoard(rnd.size, rnd.size),
		South: cli,
		North: nil,
	})
}

// Nothing has to be done if a client died
func (rnd *random) Forget(_ *Tournament, cli *Client) {
	log.Println(cli, "was disqualified")
}

// Record if the client managed to beat a random agent
func (rnd *random) Record(t *Tournament, g *Game) {
	cli := g.South

	if g.Outcome == WIN {
		log.Println(cli, "managed to beat the random agent")
	} else {
		// If the agent did not manage to beat random, it is
		// removed from the participant list
		for i := range t.participants {
			if cli != t.participants[i] {
				continue
			}

			t.participants[i] = t.participants[len(t.participants)-1]
			t.participants = t.participants[:len(t.participants)-1]
			break
		}

		log.Println(cli, "failed to beat the random agent")
	}
	rnd.done[cli] = struct{}{}
}

// Check if a tournament is over
func (rnd *random) Over(t *Tournament) bool {
	return len(t.participants) <= len(rnd.done)
}

// Nothing to be done when a tournament finishes
func (*random) Deinit(*Tournament) {}

type singleElim struct {
	// List of clients that have been eliminated
	elim []*Client
	// Board size used during the tournament
	size uint
}

func (se *singleElim) String() string {
	return "single-elimination"
}

func (se *singleElim) eliminated(cli *Client) bool {
	for _, ilc := range se.elim {
		if cli == ilc {
			return true
		}
	}
	return false
}

// Find and plan possible matches
func (se *singleElim) start(t *Tournament) {
	for i := 0; i < len(t.participants); i++ {
		cli := t.participants[i]
		if se.eliminated(cli) || t.isActive(cli) {
			continue
		}
		for j := i + 1; j < len(t.participants); j++ {
			ilc := t.participants[i]
			if se.eliminated(ilc) || t.isActive(ilc) {
				continue
			}

			t.startGame(&Game{
				Board: makeBoard(se.size, se.size),
				South: cli,
				North: ilc,
			})
			break
		}
	}
}

// Start games whenever someone is ready
func (se *singleElim) Ready(t *Tournament, _ *Client) {
	se.start(t)
}

// Mark a client as dead or eliminated
func (se *singleElim) Forget(_ *Tournament, cli *Client) {
	se.elim = append(se.elim, cli)
}

// Record the outcome of a game
func (se *singleElim) Record(t *Tournament, g *Game) {
	o, cli := g.Result()
	if o == RESIGN || o == LOSS {
		se.Forget(t, cli)
	}

	if se.Over(t) {
		return
	}

	se.start(t)
}

// Check if a tournament is over
func (se *singleElim) Over(t *Tournament) bool {
	return len(t.participants) == len(se.elim)+1
}

// Remove all clients that have lost
func (se *singleElim) Deinit(t *Tournament) {
	for _, cli := range t.participants {
		if !se.eliminated(cli) {
			t.participants = []*Client{cli}
			return
		}
	}
	panic("All agents have been eliminated")
}
