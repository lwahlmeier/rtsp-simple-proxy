package main

import (
	"fmt"
	"net/http"

	// _ "net/http/pprof"
	"os"
	"strings"
	"time"

	"github.com/PremiereGlobal/stim/pkg/stimlog"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v2"
)

var Version string
var config = viper.New()

var log2 stimlog.StimLogger = stimlog.GetLogger() //Fix for deadlock
var log stimlog.StimLogger = stimlog.GetLoggerWithPrefix("[Main]")

type trackFlow int

const (
	_TRACK_FLOW_RTP trackFlow = iota
	_TRACK_FLOW_RTCP
)

type track struct {
	rtpPort  int
	rtcpPort int
}

type streamProtocol int

const (
	_STREAM_PROTOCOL_UDP streamProtocol = iota
	_STREAM_PROTOCOL_TCP
)

func (s streamProtocol) String() string {
	if s == _STREAM_PROTOCOL_UDP {
		return "udp"
	}
	return "tcp"
}

type streamConf struct {
	Url      string `yaml:"url"`
	Protocol string `yaml:"protocol"`
}

type conf struct {
	ReadTimeout  string `yaml:"readTimeout"`
	WriteTimeout string `yaml:"writeTimeout"`
	Server       struct {
		Protocols []string `yaml:"protocols"`
		RtspPort  int      `yaml:"rtspPort"`
		RtpPort   int      `yaml:"rtpPort"`
		RtcpPort  int      `yaml:"rtcpPort"`
		ReadUser  string   `yaml:"readUser"`
		ReadPass  string   `yaml:"readPass"`
	} `yaml:"server"`
	Streams map[string]streamConf `yaml:"streams"`
}

func loadConf(confPath string) (*conf, error) {
	if confPath == "stdin" {
		var ret conf
		err := yaml.NewDecoder(os.Stdin).Decode(&ret)
		if err != nil {
			return nil, err
		}

		return &ret, nil

	} else {
		f, err := os.Open(confPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		var ret conf
		err = yaml.NewDecoder(f).Decode(&ret)
		if err != nil {
			return nil, err
		}

		return &ret, nil
	}
}

type args struct {
	version  bool
	confPath string
}

type program struct {
	conf         conf
	readTimeout  time.Duration
	writeTimeout time.Duration
	protocols    map[streamProtocol]struct{}
	streams      map[string]*stream
	tcpl         *serverTcpListener
	udplRtp      *serverUdpListener
	udplRtcp     *serverUdpListener
}

func parseArgs(cmd *cobra.Command, args []string) {
	if config.GetBool("version") {
		fmt.Printf("%s\n", Version)
		os.Exit(0)
	}
	lc := stimlog.GetLoggerConfig()
	switch strings.ToLower(config.GetString("loglevel")) {
	case "info":
		lc.SetLevel(stimlog.InfoLevel)
	case "warn":
		lc.SetLevel(stimlog.WarnLevel)
	case "debug":
		lc.SetLevel(stimlog.DebugLevel)
	case "trace":
		lc.SetLevel(stimlog.TraceLevel)
	}
}

func newProgram(config *viper.Viper) (*program, error) {

	conf, err := loadConf(config.GetString("config"))
	if err != nil {
		return nil, err
	}

	if conf.ReadTimeout == "" {
		conf.ReadTimeout = "5s"
	}
	if conf.WriteTimeout == "" {
		conf.WriteTimeout = "5s"
	}
	if len(conf.Server.Protocols) == 0 {
		conf.Server.Protocols = []string{"tcp", "udp"}
	}
	if conf.Server.RtspPort == 0 {
		conf.Server.RtspPort = 8554
	}
	if conf.Server.RtpPort == 0 {
		conf.Server.RtpPort = 8050
	}
	if conf.Server.RtcpPort == 0 {
		conf.Server.RtcpPort = 8051
	}

	readTimeout, err := time.ParseDuration(conf.ReadTimeout)
	if err != nil {
		return nil, fmt.Errorf("unable to parse read timeout: %s", err)
	}
	writeTimeout, err := time.ParseDuration(conf.WriteTimeout)
	if err != nil {
		return nil, fmt.Errorf("unable to parse write timeout: %s", err)
	}
	protocols := make(map[streamProtocol]struct{})
	for _, proto := range conf.Server.Protocols {
		switch proto {
		case "udp":
			protocols[_STREAM_PROTOCOL_UDP] = struct{}{}

		case "tcp":
			protocols[_STREAM_PROTOCOL_TCP] = struct{}{}

		default:
			return nil, fmt.Errorf("unsupported protocol: '%v'", proto)
		}
	}

	if (conf.Server.RtpPort % 2) != 0 {
		return nil, fmt.Errorf("rtp port must be even")
	}
	if conf.Server.RtcpPort != (conf.Server.RtpPort + 1) {
		return nil, fmt.Errorf("rtcp port must be rtp port plus 1")
	}
	if len(conf.Streams) == 0 {
		return nil, fmt.Errorf("no streams provided")
	}

	log.Info("rtsp-simple-proxy {}", Version)

	p := &program{
		conf:         *conf,
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
		protocols:    protocols,
		streams:      make(map[string]*stream),
	}

	p.udplRtp, err = newServerUdpListener(p, p.conf.Server.RtpPort, _TRACK_FLOW_RTP)
	if err != nil {
		return nil, err
	}

	p.udplRtcp, err = newServerUdpListener(p, p.conf.Server.RtcpPort, _TRACK_FLOW_RTCP)
	if err != nil {
		return nil, err
	}

	p.tcpl, err = newServerTcpListener(p)
	if err != nil {
		return nil, err
	}

	for path, val := range p.conf.Streams {
		var err error
		p.streams[path], err = newStream(p, path, val)
		if err != nil {
			return nil, fmt.Errorf("error in stream '%s': %s", path, err)
		}
	}

	for _, s := range p.streams {
		go s.run()
	}

	go p.udplRtp.run()
	go p.udplRtcp.run()
	go p.tcpl.run()
	return p, nil
}

func (p *program) close() {
	for _, s := range p.streams {
		s.close()
	}

	p.tcpl.close()
	p.udplRtcp.close()
	p.udplRtp.close()
}

func main() {
	lc := stimlog.GetLoggerConfig()
	lc.SetLevel(stimlog.InfoLevel)
	lc.ForceFlush(true)

	config.SetEnvPrefix("rtsp")
	config.AutomaticEnv()
	if Version == "" || Version == "lastest" {
		Version = "unknown"
	}
	var cmd = &cobra.Command{
		Use:   "rtsp-simple-proxy",
		Short: "rtsp-simple-proxy",
		Long:  "rtsp-simple-proxy",
		Run:   parseArgs,
	}

	cmd.PersistentFlags().String("config", "config.yaml", "Config file path")
	config.BindPFlag("config", cmd.PersistentFlags().Lookup("config"))
	cmd.PersistentFlags().String("metricsPort", "9347", "prometheus metrics port")
	config.BindPFlag("metricsPort", cmd.PersistentFlags().Lookup("metricsPort"))
	cmd.PersistentFlags().String("metricsIP", "", "IP to listen to metrics on (default is 0.0.0.0 or all IPs)")
	config.BindPFlag("metricsIP", cmd.PersistentFlags().Lookup("metricsIP"))
	cmd.PersistentFlags().String("loglevel", "info", "level to show logs at (warn, info, debug, trace)")
	config.BindPFlag("loglevel", cmd.PersistentFlags().Lookup("loglevel"))
	cmd.PersistentFlags().Bool("version", false, "prints the version the exits")
	config.BindPFlag("version", cmd.PersistentFlags().Lookup("version"))

	cmd.Execute()
	p, err := newProgram(config)
	if err != nil {
		log.Fatal(err)
	}

	mip := config.GetString("metricsIP")
	if mip == "0.0.0.0" {
		mip = ""
	}
	mipp := fmt.Sprintf("%s:%s", mip, config.GetString("metricsPort"))
	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(mipp, nil)
	p.close()
}
