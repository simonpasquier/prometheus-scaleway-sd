// Copyright 2018 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/discovery/targetgroup"
	"github.com/scaleway/go-scaleway"
	"github.com/scaleway/go-scaleway/types"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	a            = kingpin.New("sd adapter usage", "Tool to generate Prometheus file_sd target files for Scaleway.")
	outputf      = a.Flag("output.file", "The output filename for file_sd compatible file.").Default("scw.json").String()
	organization = a.Flag("scw.organization", "The Scaleway organization.").Default("").String()
	region       = a.Flag("scw.region", "The Scaleway region.").Default("par1").String()
	token        = a.Flag("scw.token", "The authentication token.").Default("").String()
	tokenf       = a.Flag("scw.token-file", "The authentication token file.").Default("").String()
	logger       log.Logger

	// identifierLabel is the name for the label containing the server's identifier.
	identifierLabel = model.MetaLabelPrefix + "scaleway_identifier"
	// nodeLabel is the name for the label containing the server's name.
	nameLabel = model.MetaLabelPrefix + "scaleway_name"
	// tagLabel is the name for the label containing all the server's tags.
	tagsLabel = model.MetaLabelPrefix + "scaleway_tags"
	// TODO: add more labels
)

type scwLogger struct {
	log.Logger
}

func (l *scwLogger) LogHTTP(*http.Request) {
}
func (l *scwLogger) Fatalf(format string, v ...interface{}) {
	level.Error(l).Log("msg", fmt.Sprintf(format, v))
}
func (l *scwLogger) Debugf(format string, v ...interface{}) {
	level.Debug(l).Log("msg", fmt.Sprintf(format, v))
}
func (l *scwLogger) Infof(format string, v ...interface{}) {
	level.Info(l).Log("msg", fmt.Sprintf(format, v))
}
func (l *scwLogger) Warnf(format string, v ...interface{}) {
	level.Warn(l).Log("msg", fmt.Sprintf(format, v))
}

// scwDiscoverer retrieves target information from the Scaleway API.
type scwDiscoverer struct {
	client    *api.ScalewayAPI
	port      int
	refresh   int
	separator string

	logger log.Logger
}

func (d *scwDiscoverer) createTarget(srv *types.ScalewayServer) (*targetgroup.Group, error) {
	tags := d.separator + strings.Join(srv.Tags, d.separator) + d.separator
	addr := net.JoinHostPort(srv.PrivateIP, fmt.Sprintf("%d", d.port))

	return &targetgroup.Group{
		Source: fmt.Sprintf("scaleway/%s", srv.Identifier),
		Targets: []model.LabelSet{
			model.LabelSet{
				model.AddressLabel: model.LabelValue(addr),
			},
		},
		Labels: model.LabelSet{
			model.AddressLabel:               model.LabelValue(addr),
			model.LabelName(identifierLabel): model.LabelValue(srv.Identifier),
			model.LabelName(nameLabel):       model.LabelValue(srv.Name),
			model.LabelName(tagsLabel):       model.LabelValue(tags),
		},
	}, nil
}

func (d *scwDiscoverer) Run(ctx context.Context, ch chan<- []*targetgroup.Group) {
	for c := time.Tick(time.Duration(d.refresh) * time.Second); ; {
		srvs, err := d.client.GetServers(false, 0)

		var tgs []*targetgroup.Group
		for _, s := range *srvs {
			tg, err := d.createTarget(&s)
			if err != nil {
				level.Error(d.logger).Log("msg", "Error processing server", "server", s.Name, "id", s.Identifier, "err", err)
				break
			}
			tgs = append(tgs, tg)
		}
		if err == nil {
			ch <- tgs
		}
		// Wait for ticker or exit when ctx is closed.
		select {
		case <-c:
			continue
		case <-ctx.Done():
			return
		}
	}
}

func main() {
	a.HelpFlag.Short('h')

	_, err := a.Parse(os.Args[1:])
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	logger = log.NewSyncLogger(log.NewLogfmtLogger(os.Stdout))
	logger = log.With(logger, "ts", log.DefaultTimestampUTC, "caller", log.DefaultCaller)

	if *token == "" && *tokenf == "" {
		fmt.Println("need to pass --scw.token or --scw.token-file")
		os.Exit(1)
	}
	if *tokenf != "" {
		if *token != "" {
			fmt.Println("cannot pass --scw.token and --scw.token-file at the same time")
			os.Exit(1)
		}
		b, err := ioutil.ReadFile(*tokenf)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		*token = strings.TrimSpace(strings.TrimRight(string(b), "\n"))
	}

	client, err := api.NewScalewayAPI(
		*organization,
		*token,
		"Prometheus/SD-Agent",
		*region,
		func(s *api.ScalewayAPI) {
			s.Logger = &scwLogger{logger}
		},
	)
	if err != nil {
		fmt.Println("failed to create Scaleway API client:", err)
		os.Exit(1)
	}
	err = client.CheckCredentials()
	if err != nil {
		fmt.Println("failed to check Scaleway credentials:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	disc := &scwDiscoverer{
		client:    client,
		port:      80,
		refresh:   30,
		separator: ",",
		logger:    logger,
	}
	sdAdapter := NewAdapter(ctx, *outputf, "scalewaySD", disc, logger)
	sdAdapter.Run()

	<-ctx.Done()
}
