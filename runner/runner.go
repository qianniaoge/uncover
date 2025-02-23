package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/projectdiscovery/gologger"
	"github.com/projectdiscovery/stringsutil"
	"github.com/projectdiscovery/uncover/uncover"
	"github.com/projectdiscovery/uncover/uncover/agent/censys"
	"github.com/projectdiscovery/uncover/uncover/agent/fofa"
	"github.com/projectdiscovery/uncover/uncover/agent/shodan"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// Runner is an instance of the uncover enumeration
// client used to orchestrate the whole process.
type Runner struct {
	options *Options
}

// NewRunner creates a new runner struct instance by parsing
// the configuration options, configuring sources, reading lists
// and setting up loggers, etc.
func NewRunner(options *Options) (*Runner, error) {
	runner := &Runner{options: options}
	return runner, nil
}

// RunEnumeration runs the subdomain enumeration flow on the targets specified
func (r *Runner) Run(ctx context.Context, query ...string) error {
	if !r.options.Provider.HasKeys() {
		return errors.New("no keys provided")
	}
	var agents []uncover.Agent
	// declare clients
	for _, engine := range r.options.Engine {
		var (
			agent uncover.Agent
			err   error
		)
		switch engine {
		case "shodan":
			agent, err = shodan.New()
		case "censys":
			agent, err = censys.New()
		case "fofa":
			agent, err = fofa.New()
		default:
			err = errors.New("unknown agent type")
		}
		if err != nil {
			return err
		}
		agents = append(agents, agent)
	}

	// open the output file - always overwrite
	outputWriter, err := NewOutputWriter()
	if err != nil {
		return err
	}
	outputWriter.AddWriters(os.Stdout)
	if r.options.OutputFile != "" {
		outputFile, err := os.Create(r.options.OutputFile)
		if err != nil {
			return err
		}
		defer outputFile.Close()
		outputWriter.AddWriters(outputFile)
	}

	// enumerate
	var wg sync.WaitGroup

	for _, q := range query {
		uncoverQuery := &uncover.Query{
			Query: q,
			Limit: r.options.Limit,
		}
		for _, agent := range agents {
			wg.Add(1)
			go func(agent uncover.Agent, uncoverQuery *uncover.Query) {
				defer wg.Done()

				keys := r.options.Provider.GetKeys()
				if keys.Empty() {
					gologger.Error().Label(agent.Name()).Msgf("empty keys\n")
					return
				}
				session, err := uncover.NewSession(&keys, r.options.Timeout)
				if err != nil {
					gologger.Error().Label(agent.Name()).Msgf("couldn't create new session: %s\n", err)
				}

				ch, err := agent.Query(session, uncoverQuery)
				if err != nil {
					gologger.Warning().Msgf("%s\n", err)
					return
				}
				for result := range ch {
					result.Timestamp = time.Now().Unix()
					switch {
					case result.Error != nil:
						gologger.Warning().Label(agent.Name()).Msgf("%s\n", result.Error.Error())
					case r.options.JSON:
						data, err := json.Marshal(result)
						if err != nil {
							continue
						}
						gologger.Verbose().Label(agent.Name()).Msgf("%s\n", string(data))
						outputWriter.Write(data)
					default:
						port := fmt.Sprint(result.Port)
						replacer := strings.NewReplacer(
							"ip", result.IP,
							"host", result.Host,
							"port", port,
						)
						outData := replacer.Replace(r.options.OutputFields)
						searchFor := []string{result.IP, port}
						if result.Host != "" {
							searchFor = append(searchFor, result.Host)
						}
						// send to output if any of the field got replaced
						if stringsutil.ContainsAny(outData, searchFor...) {
							gologger.Verbose().Label(agent.Name()).Msgf("%s\n", outData)
							outputWriter.WriteString(outData)
						}
					}

				}
			}(agent, uncoverQuery)
		}
	}

	wg.Wait()
	return nil
}
