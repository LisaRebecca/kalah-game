package main

import (
	"database/sql"
	"log"
	"sync"

	_ "embed"

	_ "github.com/mattn/go-sqlite3"
)

type DBAction func(*sql.DB) error

//go:embed sql/insert-move.sql
var sqlInsertMoveSrc string
var sqlInsertMove *sql.Stmt

func (game *Game) UpdateDatabase(db *sql.DB) error {
	res, err := sqlInsertGame.Exec(game.North.Id, game.South.Id)
	if err != nil {
		return err
	}
	game.Id, err = res.LastInsertId()
	return err
}

//go:embed sql/insert-game.sql
var sqlInsertGameSrc string
var sqlInsertGame *sql.Stmt

func (mov *Move) UpdateDatabase(db *sql.DB) error {
	// Do not save a move if the game has been invalidated
	if mov.game == nil {
		return nil
	}
	_, err := sqlInsertGame.Exec(mov.cli.comment, mov.cli.Id, mov.game.Id)
	return err
}

//go:embed sql/insert-agent.sql
var sqlInsertAgentSrc string
var sqlInsertAgent *sql.Stmt

//go:embed sql/select-agent.sql
var sqlSelectAgentSrc string
var sqlSelectAgent *sql.Stmt

func (cli *Client) UpdateDatabase(wait *sync.WaitGroup) DBAction {
	log.Print("Request to save", cli)
	return func(db *sql.DB) error {
		log.Print("Starting to save", cli)
		res, err := sqlInsertAgent.Exec(
			cli.token, cli.Name, cli.Descr,
			cli.Name, cli.Descr, cli.Score)
		if err != nil {
			return err
		}
		cli.Id, err = res.LastInsertId()

		err = sqlSelectAgent.QueryRow(cli.Id).Scan(&cli.Score, nil, nil)
		if err != nil {
			cli.kill <- true
		}
		if wait != nil {
			wait.Done()
		}
		return nil
	}
}

func QueryAgent(aid uint, c chan<- *Client) DBAction {
	return func(db *sql.DB) error {
		var cli Client

		err := sqlSelectAgent.QueryRow(aid).Scan(&cli.Score, nil, nil)
		if err != nil {
			close(c)
		} else {
			c <- &cli
		}

		return err
	}
}

//go:embed sql/select-game.sql
var sqlSelectGameSrc string
var sqlSelectGame *sql.Stmt

//go:embed sql/select-moves.sql
var sqlSelectMovesSrc string
var sqlSelectMoves *sql.Stmt

func QueryGame(gid uint, c chan<- *Game) DBAction {
	return func(db *sql.DB) (err error) {
		var (
			naid, said   int
			north, south Agent
			game         Game
		)

		err = sqlSelectGame.QueryRow(gid).Scan(
			&naid, &said, &game.Result, &game.start,
		)
		if err != nil {
			close(c)
			return
		}

		err = sqlSelectAgent.QueryRow(naid).Scan(
			&north.Name, &north.Descr, &north.Score,
		)
		if err != nil {
			close(c)
			return
		}
		game.North = &Client{Agent: north}

		err = sqlSelectAgent.QueryRow(said).Scan(
			&south.Name, &south.Descr, &south.Score,
		)
		if err != nil {
			close(c)
			return
		}
		game.South = &Client{Agent: south}

		rows, err := sqlSelectMoves.Query(gid)
		if err != nil {
			close(c)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var (
				aid  int
				move Move
				side Side
			)

			err = rows.Scan(&aid, &move.comm, &move.pit)
			if err != nil {
				close(c)
				return
			}

			move.game = &game
			switch aid {
			case naid:
				move.cli = game.North
				side = SideNorth
			case said:
				move.cli = game.South
				side = SideSouth
			default:
				panic("Invalid move in database")
			}

			// TODO Ensure the next move is on the right
			// side, by checking the return value in the
			// next iteration.
			game.Board.Sow(side, move.pit)

			game.Moves = append(game.Moves, move)
		}

		c <- &game
		return
	}
}

//go:embed sql/select-games.sql
var sqlSelectGamesSrc string
var sqlSelectGames *sql.Stmt

func QueryGames(c chan<- *Game, page uint) DBAction {
	return func(db *sql.DB) (err error) {
		rows, err := sqlSelectGames.Query(page)
		if err != nil {
			close(c)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var (
				game         Game
				naid, said   int
				north, south Agent
			)

			err = rows.Scan(
				&game.Id, &naid, &said, &game.Result, &game.start,
			)
			if err != nil {
				close(c)
				return
			}

			err = sqlSelectAgent.QueryRow(naid).Scan(
				&north.Name, nil, &north.Score,
			)
			if err != nil {
				close(c)
				return
			}
			game.North = &Client{Agent: north}

			err = sqlSelectAgent.QueryRow(said).Scan(
				&south.Name, nil, &south.Score,
			)
			if err != nil {
				close(c)
				return
			}
			game.South = &Client{Agent: south}

			c <- &game
		}
		return
	}
}

//go:embed sql/select-agents.sql
var sqlSelectAgentsSrc string
var sqlSelectAgents *sql.Stmt

func QueryAgents(c chan<- *Agent, page uint) DBAction {
	return func(db *sql.DB) (err error) {
		rows, err := sqlSelectAgents.Query(page)
		if err != nil {
			close(c)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var agent Agent

			err = rows.Scan(&agent.Id, &agent.Name, &agent.Score)
			if err != nil {
				close(c)
				return
			}

			c <- &agent
		}
		return
	}
}

var dbact = make(chan DBAction, 64)

//go:embed sql/create-agent.sql
var sqlCreateAgentSrc string
var sqlCreateAgent *sql.Stmt

//go:embed sql/create-game.sql
var sqlCreateGameSrc string
var sqlCreateGame *sql.Stmt

//go:embed sql/create-move.sql
var sqlCreateMoveSrc string
var sqlCreateMove *sql.Stmt

func manageDatabase(file string) {
	db, err := sql.Open("sqlite3", file+"?mode=rwc&_journal=wal")
	if err != nil {
		log.Fatal(err)
	}
	defer close(dbact)
	defer db.Close()

	// Prepare statements
	for _, ent := range []struct {
		sql  string
		stmt **sql.Stmt
	}{
		{sqlInsertMoveSrc, &sqlInsertMove},
		{sqlInsertGameSrc, &sqlInsertGame},
		{sqlInsertAgentSrc, &sqlInsertAgent},
		{sqlSelectAgentSrc, &sqlSelectAgent},
		{sqlSelectGamesSrc, &sqlSelectGames},
		{sqlSelectAgentsSrc, &sqlSelectAgents},
		{sqlCreateAgentSrc, &sqlCreateAgent},
		{sqlCreateGameSrc, &sqlCreateGame},
		{sqlCreateMoveSrc, &sqlCreateMove},
	} {
		*ent.stmt, err = db.Prepare(ent.sql)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Create tables
	_, err = sqlCreateAgent.Exec()
	if err != nil {
		log.Fatal(err)
	}
	_, err = sqlCreateGame.Exec()
	if err != nil {
		log.Fatal(err)
	}
	_, err = sqlCreateMove.Exec()
	if err != nil {
		log.Fatal(err)
	}

	log.Print("Waiting for Database actions")
	for act := range dbact {
		err := act(db)
		if err != nil {
			log.Print(err)
		}
	}
	log.Print("Terminating Database manager")
}
