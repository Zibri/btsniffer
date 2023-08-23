package main

import (
	"bytes"
	"fmt"
	"unicode"
//	"io/ioutil"
	"log"
	"net"
	"os"
//	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akame-moe/btsniffer/core"
	"github.com/marksamman/bencode"
	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"
//	"go.etcd.io/etcd/pkg/fileutil"
)

var keywords []string

type tfile struct {
	name   string
	length int64
}

func (t *tfile) String() string {
	return fmt.Sprintf("{ name: %s\n, size: %d }", t.name, t.length)
}

type torrent struct {
	infohashHex string
	name        string
	length      int64
	files       []*tfile
}

func ByteCountSI(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c",
		float64(b)/float64(div), "kMGTPE"[exp])
}
/*
func (t *torrent) String() string {
	return fmt.Sprintf(
		"[%s]%d(%s)%s#%d\n",
		t.infohashHex,
		t.length,
		ByteCountSI(t.length),
		t.name,
		len(t.files),
	)
}
func (t *torrent) Pretty() string {
	return fmt.Sprintf(
		"link: %s\nname: %s\nsize: %d(%s)\nfile: %d\n",
		fmt.Sprintf("magnet:?xt=urn:btih:%s", t.infohashHex),
		t.name,
		t.length,
		ByteCountSI(t.length),
		len(t.files),
	)
}
*/

func parseTorrent(meta []byte, infohashHex string) (*torrent, error) {
	dict, err := bencode.Decode(bytes.NewBuffer(meta))
	if err != nil {
		return nil, err
	}

	t := &torrent{infohashHex: infohashHex}
	if name, ok := dict["name.utf-8"].(string); ok {
		t.name = name
	} else if name, ok := dict["name"].(string); ok {
		t.name = name
	}
	if length, ok := dict["length"].(int64); ok {
		t.length = length
	}

	var totalSize int64
	var extractFiles = func(file map[string]interface{}) {
		var filename string
		var filelength int64
		if inter, ok := file["path.utf-8"].([]interface{}); ok {
			name := make([]string, len(inter))
			for i, v := range inter {
				name[i] = fmt.Sprint(v)
			}
			filename = strings.Join(name, "/")
		} else if inter, ok := file["path"].([]interface{}); ok {
			name := make([]string, len(inter))
			for i, v := range inter {
				name[i] = fmt.Sprint(v)
			}
			filename = strings.Join(name, "/")
		}
		if length, ok := file["length"].(int64); ok {
			filelength = length
			totalSize += filelength
		}
		t.files = append(t.files, &tfile{name: filename, length: filelength})
	}

	if files, ok := dict["files"].([]interface{}); ok {
		for _, file := range files {
			if f, ok := file.(map[string]interface{}); ok {
				extractFiles(f)
			}
		}
	}

	if t.length == 0 {
		t.length = totalSize
	}
	if len(t.files) == 0 {
		t.files = append(t.files, &tfile{name: t.name, length: t.length})
	}

	return t, nil
}

type btsniffer struct {
	laddr        string
	maxFriends   int
	maxPeers     int
	secret       string
	timeout      time.Duration
	blacklist    *core.BlackList
	dir          string
	keywordFile  string
}

func (t *btsniffer) run() error {

	tokens := make(chan struct{}, t.maxPeers)

	dht, err := core.NewDHT(t.laddr, t.maxFriends)
	if err != nil {
		return err
	}

	dht.Run()

	//log.Println("torrent sniffer is running...")

	for {
		select {
		case <-dht.Announcements.Wait():
			for {
				if ac := dht.Announcements.Get(); ac != nil {
					tokens <- struct{}{}
					go t.work(ac, tokens)
					continue
				}
				break
			}
		case <-dht.Die:
			return dht.ErrDie
		}
	}

}

func (t *btsniffer) work(ac *core.Announcement, tokens chan struct{}) {
	defer func() {
		<-tokens
	}()

	peerAddr := ac.Peer.String()
	if t.blacklist.Has(peerAddr) {
		return
	}

	wire := core.NewMetaWire(string(ac.InfoHash), peerAddr, t.timeout)
	defer wire.Free()

	meta, err := wire.Fetch()
	if err != nil {
		t.blacklist.Add(peerAddr)
		return
	}

	torrent, err := parseTorrent(meta, ac.InfoHashHex)
	if err != nil {
		return
	}
	tname := strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII {
			return -1
		}
		return r
	}, torrent.name)
	log.Println(torrent.infohashHex,tname,torrent.length,ByteCountSI(torrent.length))
	//log.Println(torrent)
}

/*
func (t *btsniffer) isTorrentExist(infohashHex string) bool {
	name, _ := t.torrentPath(infohashHex)
	_, err := os.Stat(name)
	if os.IsNotExist(err) {
		return false
	}
	return err == nil
}
func (t *btsniffer) saveTorrent(infohashHex string, data []byte) error {
	name, dir := t.torrentPath(infohashHex)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	d, err := bencode.Decode(bytes.NewBuffer(data))
	if err != nil {
		return err
	}

	f, err := fileutil.TryLockFile(name, os.O_WRONLY|os.O_CREATE, 0744)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err = f.Write(bencode.Encode(map[string]interface{}{"info": d})); err != nil {
		return err
	}
	return nil
}

func (t *btsniffer) torrentPath(infohashHex string) (name string, dir string) {
	dir = path.Join(t.dir, infohashHex[:2], infohashHex[2:4])
	name = path.Join(dir, infohashHex+".torrent")
	return
}
*/
func main() {
	log.SetFlags(0)

	var addr string
	var port uint16
	var peers int
	var timeout time.Duration
	var dir string
	var verbose bool
	var friends int
	var keywordFile string

	root := &cobra.Command{
		Use:          "torsniff",
		Short:        "torsniff - A sniffer that sniffs torrents from BitTorrent network.",
		SilenceUsage: true,
	}
	root.RunE = func(cmd *cobra.Command, args []string) error {

		absDir, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		_, err = os.Stat(absDir)

		log.SetOutput(os.Stdout)

		var p [10]*btsniffer
		for i := 0; i < 3; i++ {
		p[i] = &btsniffer{
			laddr:        net.JoinHostPort(addr, strconv.Itoa(int(port)+i)),
			timeout:      timeout,
			maxFriends:   friends,
			maxPeers:     peers,
			secret:       string(core.RandBytes(20)),
			dir:          absDir,
			blacklist:    core.NewBlackList(5*time.Minute, 1000),
			keywordFile:  keywordFile,
		}
		go p[i].run()
		}

                p[3] = &btsniffer{
                        laddr:        net.JoinHostPort(addr, strconv.Itoa(int(port)+5)),
                        timeout:      timeout,
                        maxFriends:   friends,
                        maxPeers:     peers,
                        secret:       string(core.RandBytes(20)),
                        dir:          absDir,
                        blacklist:    core.NewBlackList(5*time.Minute, 1000),
                        keywordFile:  keywordFile,
                }

		return p[3].run()
	}

	root.Flags().StringVarP(&addr, "addr", "a", "0.0.0.0", "listen on given address (default all, ipv4 and ipv6)")
	root.Flags().Uint16VarP(&port, "port", "p", 6881, "listen on given port")
	root.Flags().IntVarP(&friends, "friends", "f", 500, "max friends to make with per second")
	root.Flags().IntVarP(&peers, "peers", "e", 400, "max peers to connect to download torrents")
	root.Flags().DurationVarP(&timeout, "timeout", "t", 10*time.Second, "max time allowed for downloading torrents")
	root.Flags().BoolVarP(&verbose, "verbose", "v", true, "run in verbose mode")

	if err := root.Execute(); err != nil {
		fmt.Println(fmt.Errorf("could not start: %s", err))
	}
}
