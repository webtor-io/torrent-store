package services

import (
	"bytes"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	sl "github.com/webtor-io/stoplist"
	"regexp"
	"strings"
)

var (
	re1 = regexp.MustCompile(`[^\p{L}\d]+`)
	re2 = regexp.MustCompile(`(\d+)`)
	re3 = regexp.MustCompile(`\s+`)
)

const (
	StoplistPathFlag = "stoplist-path"
)

func RegisterStoplistFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   StoplistPathFlag,
			Usage:  "stoplist path",
			EnvVar: "STOPLIST_PATH",
			Value:  "",
		},
	)
}

type Stoplist struct {
	c sl.Checker
}

func NewStoplist(c *cli.Context) (*Stoplist, error) {
	path := c.String(StoplistPathFlag)
	if path == "" {
		return nil, nil
	}
	ch, err := sl.NewRuleFromYamlFile(path)
	if err != nil {
		return nil, err
	}
	return &Stoplist{
		c: ch,
	}, nil
}

func (s *Stoplist) getData(b []byte) ([]string, error) {
	reader := bytes.NewReader(b)
	mi, err := metainfo.Load(reader)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse torrent")
	}
	i, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal torrent info")
	}
	var data []string
	data = append(data, i.Name)
	for _, file := range i.Files {
		path := file.PathUtf8
		if path == nil {
			path = file.Path
		}
		data = append(data, strings.Join(path, " "))
	}
	return data, nil
}

func (s *Stoplist) Check(b []byte) (*sl.CheckResult, error) {
	data, err := s.getData(b)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get torrent text data")
	}
	for _, d := range data {
		cr := s.c.Check(s.normalize(d))
		if cr.Found {
			return cr, nil
		}
	}
	return &sl.CheckResult{}, nil
}

func (s *Stoplist) normalize(str string) string {
	str = strings.ToLower(str)
	str = re1.ReplaceAllString(str, " ")
	str = re2.ReplaceAllString(str, " $1 ")
	str = re3.ReplaceAllString(str, " ")
	str = strings.TrimSpace(str)
	return str
}
