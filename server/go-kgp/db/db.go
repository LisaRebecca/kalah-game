// Database management
//
// Copyright (c) 2021, 2022  Philip Kaludercic
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

package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-kgp"
	"go-kgp/conf"
	"go-kgp/game"
)

//go:embed *.sql
var sql_dir embed.FS

type db struct {
	// The database connections
	read  *sql.DB
	write *sql.DB

	// The used configuration
	conf *conf.Conf

	// The SQL queries are stored under ./sql/, and they are
	// loaded by the database manager.  QUERIES are the commands
	// handle by READ, and COMMANDS are the queries handled by
	// WRITE.
	queries  map[string]*sql.Stmt
	commands map[string]*sql.Stmt
}

type user kgp.User

func (u *user) Request(*kgp.Game) (*kgp.Move, bool) {
	panic("Cannot request a move from a user")
}

func (u *user) User() *kgp.User {
	return (*kgp.User)(u)
}

func (u *user) Alive() bool {
	return false // users aren't live agents
}

func (db *db) RegisterTournament(ctx context.Context, name string) int64 {
	res, err := db.commands["insert-tournament"].ExecContext(ctx, name)
	if err != nil {
		db.conf.Log.Fatal(err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		db.conf.Log.Fatal(err)
	}
	return id
}

func (db *db) RecordScore(ctx context.Context, cli *kgp.User, game *kgp.Game, tid int64, score float64) {
	if cli == nil {
		return
	}

	_, err := db.commands["insert-score"].ExecContext(ctx,
		cli.Id, game.Id, tid, score)
	if err != nil {
		db.conf.Log.Print(err)
	}
}

func (db *db) updateDatabase(ctx context.Context, u *kgp.User, query bool) {
	var name, descr *string

	res, err := db.commands["insert-agent"].ExecContext(ctx,
		u.Token,
		u.Name,
		u.Descr,
		u.Author)
	if err != nil {
		db.conf.Log.Print(err)
		return
	}
	u.Id, err = res.LastInsertId()
	if err != nil {
		db.conf.Log.Print(err)
	}

	if query {
		err = db.queries["select-agent-token"].QueryRowContext(ctx, u.Token).Scan(
			&u.Id, &name, &descr)
		if errors.Is(err, sql.ErrNoRows) {
			db.conf.Log.Print(err)
			return
		}

		if name != nil {
			u.Name = *name
		}
		if descr != nil {
			u.Descr = *descr
		}
	}
}

func (db *db) Forget(ctx context.Context, token []byte) {
	_, err := db.commands["delete-agent"].ExecContext(ctx, token)
	if err != nil {
		db.conf.Log.Print(err)
	}
}

func (db *db) QueryUserToken(ctx context.Context, token string) *kgp.User {
	var u kgp.User
	err := db.queries["select-agent-token"].QueryRowContext(ctx, token).Scan(
		&u.Id,
		&u.Name,
		&u.Descr)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			db.conf.Log.Print(err)
		}
		return nil
	}
	return &u
}

func (db *db) queryUser(ctx context.Context, id int) (*kgp.User, error) {
	u := kgp.User{Id: int64(id)}
	return &u, db.queries["select-agent-id"].QueryRowContext(ctx, id).Scan(
		&u.Name,
		&u.Descr,
		&u.Author,
		&u.Games)
}

func (db *db) QueryUser(ctx context.Context, id int) *kgp.User {
	u, err := db.queryUser(ctx, id)
	if err != nil {
		db.conf.Log.Print(err)
		return nil
	}
	return u
}

func (db *db) QueryGame(ctx context.Context, gid int, gc chan<- *kgp.Game, mc chan<- *kgp.Move) {
	defer close(gc)
	defer close(mc)
	row := db.queries["select-game"].QueryRowContext(ctx, gid)
	g, err := db.scanGame(ctx, row.Scan)
	if err != nil {
		db.conf.Log.Print(err)
		return
	}
	gc <- g

	rows, err := db.queries["select-moves"].QueryContext(ctx, gid)
	if err != nil {
		db.conf.Log.Print(err)
		return
	}

	for rows.Next() {
		var (
			m    = &kgp.Move{}
			side bool
		)
		err = rows.Scan(&side, &m.Comment, &m.Choice, &m.Stamp)
		if err != nil {
			db.conf.Log.Print(err)
			return
		}
		m.Agent = g.Player(kgp.Side(side))

		if next, repeat := game.MoveCopy(g, m); !repeat {
			db.conf.Log.Printf("Illegal move %d on %s", m.Choice, g.State)
			break
		} else {
			g = next
		}
		m.State = g.Board.Copy()

		mc <- m
	}
	if err = rows.Err(); err != nil {
		db.conf.Log.Print(err)
	}
}

func (db *db) scanGame(ctx context.Context, scan func(dest ...interface{}) error) (game *kgp.Game, err error) {
	var (
		nid, sid   int
		size, init uint
	)

	game = &kgp.Game{}
	err = scan(
		&game.Id,
		&size, &init,
		&nid, &sid,
		&game.State,
		&game.MoveCount)
	if err != nil {
		return
	}
	game.Board = kgp.MakeBoard(size, init)

	var south, north *kgp.User
	south, err = db.queryUser(ctx, sid)
	if err != nil {
		return
	}
	north, err = db.queryUser(ctx, nid)
	if err != nil {
		return
	}

	game.North = (*user)(north)
	game.South = (*user)(south)

	return
}

func (db *db) QueryGames(ctx context.Context, aid int, c chan<- *kgp.Game, page int) {
	defer close(c)

	var (
		rows *sql.Rows
		err  error
	)
	if aid < 0 {
		rows, err = db.queries["select-games"].QueryContext(ctx, page)
	} else {
		rows, err = db.queries["select-games-by"].QueryContext(ctx,
			aid, page)
	}
	if err != nil {
		if err != sql.ErrNoRows {
			db.conf.Log.Print(err)
		}
		return
	}
	defer rows.Close()

	for rows.Next() {
		game, err := db.scanGame(ctx, rows.Scan)
		if err != nil {
			if err != sql.ErrNoRows {
				db.conf.Log.Print(err)
			}
			return
		}
		c <- game
	}
	if err = rows.Err(); err != nil {
		db.conf.Log.Print(err)
	}
}

func (db *db) QueryUsers(ctx context.Context, c chan<- *kgp.User, page int) {
	defer close(c)
	rows, err := db.queries["select-agents"].QueryContext(ctx, page, 50)
	if err != nil {
		if err != sql.ErrNoRows {
			db.conf.Log.Print(err)
		}
		return
	}
	defer rows.Close()

	for rows.Next() {
		var u kgp.User

		err = rows.Scan(
			&u.Id,
			&u.Name,
			&u.Author,
			&u.Games)
		if err != nil {
			db.conf.Log.Print(err)
			return
		}

		c <- &u
	}
	if err = rows.Err(); err != nil {
		db.conf.Log.Print(err)
		return
	}
}

func (db *db) SaveGame(ctx context.Context, game *kgp.Game) {
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		db.conf.Log.Print(err)
		return
	}
	defer tx.Rollback()

	if game.South != nil && game.South.User() != nil {
		db.saveUser(ctx, tx, game.South.User())
	}
	if game.North != nil && game.North.User() != nil {
		db.saveUser(ctx, tx, game.North.User())
	}
	if !db.saveGame(ctx, tx, game) {
		return
	}

	err = tx.Commit()
	if err != nil {
		db.conf.Log.Print(err)
	}
}

func (db *db) saveGame(ctx context.Context, tx *sql.Tx, game *kgp.Game) bool {
	if game.Id == 0 {
		north, south := game.North.User(), game.South.User()

		size, init := game.Board.Type()
		db.conf.Debug.Printf("Saving game with SID %d and NID %d",
			south.Id, north.Id)
		res, err := tx.Stmt(db.commands["insert-game"]).ExecContext(ctx,
			size, init, north.Id, south.Id, game.State.String())
		if err != nil {
			db.conf.Log.Print(err)
			return false
		}

		id, err := res.LastInsertId()
		if err != nil {
			db.conf.Log.Print(err)
			return false
		}
		game.Id = uint64(id)
	} else {
		_, err := tx.Stmt(db.commands["update-game"]).ExecContext(ctx,
			game.State.String(), game.Id)
		if err != nil {
			db.conf.Log.Print(err)
			return false
		}
	}

	return true
}

func (db *db) saveUser(ctx context.Context, tx *sql.Tx, u *kgp.User) bool {
	if u.Id != 0 {
		return true
	}

	if u.Token != "" {
		var id *int64
		var name, desc *string
		res, err := db.queries["select-agent-token"].QueryContext(ctx, u.Token)
		if err != nil {
			// FIXME: The user should be allowed to update
			//        their metadata.
			db.conf.Debug.Print(err)
			goto insert
		}
		if !res.Next() {
			goto insert
		}
		err = res.Scan(&id, &name, &desc)
		if err == nil {
			if id != nil {
				u.Id = *id
			}
			if name != nil {
				if u.Name != *name {
					goto insert
				}
				u.Name = *name
			}
			if desc != nil {
				if u.Descr != *desc {
					goto insert
				}
				u.Descr = *desc
			}
			return true
		} else {
			db.conf.Debug.Print(err)
		}
	}
insert:

	db.conf.Debug.Printf("Saving user with %q token %q", u.Name, u.Token)
	res, err := tx.Stmt(db.commands["insert-agent"]).ExecContext(ctx,
		u.Token, u.Name, u.Descr, u.Author)
	if err != nil {
		db.conf.Log.Print(err)
		return false
	}
	u.Id, err = res.LastInsertId()
	if err != nil {
		db.conf.Log.Print(err)
		return false
	}
	db.conf.Debug.Printf("Assigned user %q ID %d", u.Name, u.Id)

	return true
}

func (db *db) SaveMove(ctx context.Context, move *kgp.Move) {
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		db.conf.Log.Print(err)
		return
	}
	defer tx.Rollback()

	game := move.Game
	south, north := game.South.User(), game.North.User()
	if !db.saveUser(ctx, tx, south) {
		return
	}
	if !db.saveUser(ctx, tx, north) {
		return
	}
	if !db.saveGame(ctx, tx, game) {
		return
	}

	_, err = tx.Stmt(db.commands["insert-move"]).ExecContext(ctx,
		game.Id,
		move.Agent.User().Id,
		game.Side(move.Agent),
		move.Choice,
		move.Comment,
		move.Stamp)
	if err != nil {
		db.conf.Log.Print(err)
		return
	}

	err = tx.Commit()
	if err != nil {
		db.conf.Log.Print(err)
	}
}

func (db *db) DrawGraph(ctx context.Context, w io.Writer) error {
	res, err := db.queries["select-graph"].QueryContext(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Println("Empty response")
			return nil
		}
		return err
	}
	defer res.Close()

	seen := make(map[int]struct{})
	node := func(id int, name string) (string, error) {
		node := fmt.Sprintf("n%d", id)
		if _, ok := seen[id]; ok {
			return node, nil
		}
		if name == "" {
			name = fmt.Sprintf("Unnamed (%d)", id)
		}
		name = strings.ReplaceAll(name, `"`, `\"`)
		_, err = fmt.Fprintf(w, `%s [label="%s" href="/agent/%d"];`,
			node, name, id)
		if err != nil {
			return "", err
		}
		return node, nil
	}

	_, err = fmt.Fprintf(w, `strict digraph dominance { ratio = compress ;`)
	if err != nil {
		return err
	}

	for res.Next() {
		var (
			wname, lname string
			wid, lid     int
		)

		err = res.Scan(&wname, &wid, &lname, &lid)
		if err != nil {
			return err
		}

		t, err := node(lid, lname)
		if err != nil {
			return err
		}
		f, err := node(wid, wname)
		if err != nil {
			return err
		}

		_, err = fmt.Fprint(w, f, "->", t, ";")
		if err != nil {
			return err
		}
	}

	_, err = fmt.Fprint(w, `}`)
	if err != nil {
		return err
	}

	return nil
}

func (db *db) Start() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGUSR1)
	tick := time.NewTicker(24 * time.Hour)
	for {
		var err error
		select {
		case <-c:
			// https://www.sqlite.org/lang_vacuum.html
			_, err = db.write.Exec("VACUUM;")
		case <-tick.C:
			db.commands["delete-moves"].Exec()
			// https://www.sqlite.org/pragma.html#pragma_optimize
			_, err = db.write.Exec("PRAGMA optimize;")
		}
		if err != nil {
			db.conf.Log.Print(err)
		}
	}
}

func (db *db) Shutdown() {
	var err error

	// https://www.sqlite.org/pragma.html#pragma_optimize
	_, err = db.write.Exec("PRAGMA optimize;")
	if err != nil {
		db.conf.Log.Print(err)
	}

	err = db.write.Close()
	if err != nil {
		db.conf.Log.Print(err)
	}

	err = db.read.Close()
	if err != nil {
		db.conf.Log.Print(err)
	}
}

func (*db) String() string { return "Database Manager" }

// Initialise the database and database managers
func Prepare(config *conf.Conf) {
	fatal := config.Log.Fatal

	var err error
	read, err := sql.Open("sqlite3", config.Database)
	if err != nil {
		config.Log.Fatal(err, ": ", config.Database)
	}
	read.SetConnMaxLifetime(0)
	read.SetMaxIdleConns(1)

	write, err := sql.Open("sqlite3", config.Database)
	if err != nil {
		config.Log.Fatal(err, ": ", config.Database)
	}
	write.SetConnMaxLifetime(0)
	write.SetMaxIdleConns(1)
	write.SetMaxOpenConns(1)

	for _, pragma := range []string{
		// https://www.sqlite.org/pragma.html#pragma_journal_mode
		"journal_mode = WAL",
		// https://www.sqlite.org/pragma.html#pragma_synchronous
		"synchronous = normal",
		// https://www.sqlite.org/pragma.html#pragma_temp_store
		"temp_store = memory",
		// https://www.sqlite.org/pragma.html#pragma_mmap_size
		"mmap_size = 268435456",
		// https://www.sqlite.org/pragma.html#pragma_foreign_keys
		"foreign_keys = on",
	} {
		config.Debug.Printf("Run PRAGMA %v", pragma)
		_, err = write.Exec("PRAGMA " + pragma + ";")
		if err != nil {
			fatal(err)
		}
	}

	entries, err := sql_dir.ReadDir(".")
	if err != nil {
		fatal(err)
	}
	queries := make(map[string]*sql.Stmt)
	commands := make(map[string]*sql.Stmt)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || strings.HasPrefix(".", entry.Name()) {
			continue
		}

		base := path.Base(entry.Name())
		data, err := fs.ReadFile(sql_dir, entry.Name())
		if err != nil {
			fatal(err)
		}

		if strings.HasPrefix(base, "create-") || strings.HasPrefix(base, "run-") {
			_, err = write.Exec(string(data))
			config.Debug.Printf("Executed query %v", base)
		} else {
			query := strings.TrimSuffix(base, ".sql")
			if strings.HasPrefix(query, "select-") {
				queries[query], err = read.Prepare(string(data))
				config.Debug.Printf("Registered query %v", query)
			} else {
				commands[query], err = write.Prepare(string(data))
				config.Debug.Printf("Registered command %v", query)
			}
		}
		if err != nil {
			fatal(entry.Name(), ": ", err)
		}
	}

	if len(queries) == 0 {
		panic("No queries loaded")
	}

	var man conf.DatabaseManager = &db{
		read:     read,
		write:    write,
		queries:  queries,
		commands: commands,
		conf:     config,
	}
	config.Register(man)
}
