package services

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/urfave/cli"
	sl "github.com/webtor-io/stoplist"
)

var (
	re1 = regexp.MustCompile(`[^\p{L}\d]+`)
	re2 = regexp.MustCompile(`(\d+)`)
	re3 = regexp.MustCompile(`\s+`)

	// stoplistBlocksTotal counts torrents rejected at intake by the
	// abuse stoplist, labelled by which main-rule line fired. Pairs
	// with the helmfile-managed stoplist.yaml — if `main:` rule order
	// changes there, update mainRuleLabels below.
	stoplistBlocksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "torrent_store_stoplist_blocks_total",
		Help: "Torrents rejected at intake by the abuse stoplist, labelled by which main-rule line fired.",
	}, []string{"rule"})

	// mainRuleLabels maps the library's "line index N" Stack[0] to a
	// human-readable Prometheus label. Index order MUST match the
	// `main:` list in helmfile/values/torrent-store/stoplist.yaml.
	mainRuleLabels = []string{
		"stopwords",  // line 0: {stopwords}
		"age_sexual", // line 1: {age}+{sexual}
		"age_name",   // line 2: {age}+{name}
	}
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
	// Tracker URLs + comment + creator — CSAM-distribution torrents
	// often have neutral filenames but advertise the source forum
	// through announce list or comment ("hash on cpchans.xyz", etc.).
	// Feed these as additional input so existing stoplist rules
	// (cpack, brand names, cp+context regex) catch them too.
	if mi.Announce != "" {
		data = append(data, mi.Announce)
	}
	for _, tier := range mi.AnnounceList {
		for _, url := range tier {
			data = append(data, url)
		}
	}
	if mi.Comment != "" {
		data = append(data, mi.Comment)
	}
	if mi.CreatedBy != "" {
		data = append(data, mi.CreatedBy)
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
			stoplistBlocksTotal.WithLabelValues(ruleLabel(cr)).Inc()
			return cr, nil
		}
	}
	return &sl.CheckResult{}, nil
}

// ruleLabel extracts a human-readable Prometheus label from the
// stoplist library's CheckResult. Stack[0] for a main-rule match is
// always "line index N" (see github.com/webtor-io/stoplist lineRule
// implementation); we map N to a friendly label via mainRuleLabels.
func ruleLabel(cr *sl.CheckResult) string {
	if cr == nil || !cr.Found || len(cr.Stack) == 0 {
		return "unknown"
	}
	var idx int
	if _, err := fmt.Sscanf(cr.Stack[0], "line index %d", &idx); err != nil {
		return "unknown"
	}
	if idx < 0 || idx >= len(mainRuleLabels) {
		return fmt.Sprintf("line_%d", idx)
	}
	return mainRuleLabels[idx]
}

func (s *Stoplist) normalize(str string) string {
	str = strings.ToLower(str)
	str = re1.ReplaceAllString(str, " ")
	str = re2.ReplaceAllString(str, " $1 ")
	str = re3.ReplaceAllString(str, " ")
	str = strings.TrimSpace(str)
	return str
}
