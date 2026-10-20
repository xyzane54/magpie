package routeracceptance

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"
)

// ParseOptions accepts only fixed-suite inputs. Prompts, endpoints, arbitrary
// environment overrides and capability acceptance callbacks are not CLI inputs.
func ParseOptions(args []string, out io.Writer) (Options, error) {
	o := Options{}
	f := flag.NewFlagSet("diagnose-local", flag.ContinueOnError)
	f.SetOutput(out)
	f.StringVar(&o.Model, "model", "spark-x2.5-1.7B", "fixed catalog model ID")
	f.StringVar(&o.ModelFile, "model-file", "", "complete pinned model cache file")
	f.StringVar(&o.TemplateFile, "template-file", "", "complete pinned template cache file")
	f.StringVar(&o.RuntimeArchive, "runtime-archive", "", "complete pinned runtime archive cache file")
	f.StringVar(&o.DataDir, "data-dir", "", "explicit isolated data directory")
	f.StringVar(&o.Output, "output", "", "JSON path in a new evidence directory")
	f.DurationVar(&o.LoadTimeout, "load-timeout", time.Minute, "fixed production load budget")
	f.DurationVar(&o.GenerationTimeout, "generation-timeout", 2*time.Minute, "fixed production generation budget")
	f.DurationVar(&o.InstallTimeout, "install-timeout", 10*time.Minute, "bounded explicit install wait")
	f.BoolVar(&o.PrepareOnly, "prepare-only", false, "startup and shutdown without installing or loading")
	f.BoolVar(&o.InstallOnly, "install-only", false, "explicit verified install without loading a model")
	f.BoolVar(&o.BusinessPreacceptance, "business-preacceptance", false, "run fixed judgment, tool-round and W5 privacy diagnostics on the owned Manager")
	f.StringVar(&o.PrivacyDiagnostic, "privacy-diagnostic", "", "fixed 1.7B synthetic privacy suite: representative or full; no capability acceptance")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 {
		return o, errors.New("diagnose-local accepts named fixed-suite options only")
	}
	if o.LoadTimeout != time.Minute || o.GenerationTimeout != 2*time.Minute {
		return o, errors.New("product diagnostics retain the production load and generation budgets")
	}
	if err := validatePrivacyDiagnostic(o); err != nil {
		return o, err
	}
	return o, nil
}

func WriteReport(path string, report Report, runErr error) error {
	if runErr != nil {
		report.Error = runErr.Error()
	}
	if report.Product && !report.reserved {
		return errors.New("evidence directory was not reserved; existing evidence will not be changed")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return errors.Join(encoder.Encode(report), f.Close())
}
