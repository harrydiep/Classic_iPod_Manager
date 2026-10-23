// Command itdbtool is a small diagnostics helper for Classic iPod Manager's iTunesDB
// engine. Usage:
//
//	itdbtool dump <iTunesDB>
//	itdbtool rewrite <in iTunesDB> <out iTunesDB>
package main

import (
	"fmt"
	"os"

	"podmanager/internal/itdb"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: itdbtool dump <iTunesDB> | rewrite <in> <out>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[2])
	check(err)
	db, err := itdb.Parse(data)
	check(err)
	switch os.Args[1] {
	case "dump":
		fmt.Printf("tracks=%d playlists=%d\n", len(db.Tracks), len(db.Playlists))
		for _, t := range db.Tracks {
			fmt.Printf("T id=%d title=[%s] artist=[%s] album=[%s] genre=[%s] path=[%s] size=%d len=%d trk=%d/%d year=%d rating=%d plays=%d hdr=0x%x\n",
				t.ID(), t.Title(), t.Artist(), t.Album(), t.Genre(), t.Location(), t.Size(), t.LengthMs(),
				t.TrackNumber(), t.TrackCount(), t.Year(), t.Rating(), t.PlayCount(), t.HeaderLen())
		}
		for _, p := range db.Playlists {
			fmt.Printf("P name=[%s] master=%v podcast=%v smart=%v n=%d :", p.Name, p.IsMaster(), p.IsPodcast(), p.IsSmart(), len(p.Items))
			for _, it := range p.Items {
				fmt.Printf(" %d", it.TrackID)
			}
			fmt.Println()
		}
	case "rewrite":
		if len(os.Args) < 4 {
			check(fmt.Errorf("missing output path"))
		}
		check(os.WriteFile(os.Args[3], db.Bytes(), 0o644))
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
