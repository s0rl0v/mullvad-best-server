package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-ping/ping"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type MullvadServerDTO struct {
	Hostname         string `json:"hostname"`
	CountryCode      string `json:"country_code"`
	CountryName      string `json:"country_name"`
	CityCode         string `json:"city_code"`
	CityName         string `json:"city_name"`
	Active           bool   `json:"active"`
	Owned            bool   `json:"owned"`
	Provider         string `json:"provider"`
	Ipv4AddrIn       string `json:"ipv4_addr_in"`
	Ipv6AddrIn       string `json:"ipv6_addr_in"`
	NetworkPortSpeed int    `json:"network_port_speed"`
	Pubkey           string `json:"pubkey"`
	MultihopPort     int    `json:"multihop_port"`
	SocksName        string `json:"socks_name"`
}

type MullvadServer struct {
	MullvadServerDTO
	Duration time.Duration
}

type ByLatency []*MullvadServer

func (a ByLatency) Len() int           { return len(a) }
func (a ByLatency) Less(i, j int) bool { return a[i].Duration < a[j].Duration }
func (a ByLatency) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

var ErrInvalidPing = fmt.Errorf("0s ping detected")

func main() {
	var outputFlag = flag.String("o", "", "Output format. 'json' outputs server json")
	var countryFlag = flag.String("c", "", "Server country code, e.g. ch for Switzerland")
	var excludeCountriesFlag = flag.String("e", "", "Exclude servers from these countries (e.g. 'us,se')")
	var topCountFlag = flag.String("s", "10", "Set custom limit for top latency servers output")
	var typeFlag = flag.String("t", "wireguard", "Server type, e.g. wireguard")
	var logLevel = flag.String("l", "info", "Log level. Allowed values: trace, debug, info, warn, error, fatal, panic")
	flag.Parse()

	level, err := zerolog.ParseLevel(*logLevel)
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to set log level")
	}
	zerolog.SetGlobalLevel(level)

	topLimit, err := strconv.Atoi(*topCountFlag)
	if err != nil {
		log.Fatal().Err(err).Msg("-s flag should not contain characters, only numbers")
	}

	servers := getServers(*typeFlag)
	measuredServers, err := measureServersLatency(servers, *countryFlag, *excludeCountriesFlag)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to measure ping")
	}

	sort.Sort(ByLatency(measuredServers))

	if len(measuredServers) < topLimit {
		topLimit = len(measuredServers)
	}
	for _, server := range measuredServers[:topLimit] {
		log.Debug().Interface("server", server).Msg("Best latency server found.")
		hostname := strings.TrimSuffix(server.Hostname, "-wireguard")
		if *outputFlag != "json" {
			fmt.Printf("%s: %s\n", hostname, server.Duration.String())
		} else {
			serverJson, err := json.Marshal(server)
			if err != nil {
				log.Fatal().Err(err).Msg("Couldn't marshal server information to Json")
			}
			fmt.Println(string(serverJson))
		}
	}
}

func getServers(serverType string) (servers []*MullvadServerDTO) {
	var responseBody []byte

	resp, err := http.Get("https://api.mullvad.net/www/relays/" + serverType + "/")
	if err != nil {
		log.Error().Err(err).Msg("Mullvad API not responding, falling back to local server list backup")
		responseBody, err = os.ReadFile("wireguard_servers.json")
		if err != nil {
			log.Fatal().Err(err).Msg("Can't find servers backup file")
		}
	} else {
		responseBody, err = ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to read the body")
		}
		defer resp.Body.Close()
	}

	err = json.Unmarshal(responseBody, &servers)
	if err != nil {
		log.Fatal().Err(err).Msg("couldn't unmarshall server json")
	}
	return
}

func measureServersLatency(servers []*MullvadServerDTO, country string, excludedCountriesStr string) (measuredServers []*MullvadServer, err error) {
	for _, server := range servers {
		if (!server.Active) ||
			server.CountryCode != country && country != "" ||
			strings.Contains(excludedCountriesStr, server.CountryCode) {
			continue
		}
		measuredServer, err := serverLatency(*server)
		if err != nil {
			log.Error().Err(err)
			continue
		}
		measuredServers = append(measuredServers, measuredServer)
	}
	return
}

//goland:noinspection GoBoolExpressions
func serverLatency(s MullvadServerDTO) (*MullvadServer, error) {
	pinger, err := ping.NewPinger(s.Ipv4AddrIn)
	pinger.Timeout = time.Second
	if runtime.GOOS == "windows" {
		pinger.SetPrivileged(true)
	}
	pinger.Count = 1
	if err != nil {
		return &MullvadServer{MullvadServerDTO: s, Duration: time.Second * 999}, err
	}
	var duration time.Duration
	pinger.OnRecv = func(pkt *ping.Packet) {
		log.Debug().Str("Server", s.Hostname).IPAddr("IP", pkt.IPAddr.IP).Dur("RTT", pkt.Rtt).Msg("Added server latency.")
		duration = pkt.Rtt
	}
	err = pinger.Run()
	if err != nil {
		return nil, err
	}
	if duration == 0 {
		return nil, ErrInvalidPing
	}
	return &MullvadServer{MullvadServerDTO: s, Duration: duration}, err
}
