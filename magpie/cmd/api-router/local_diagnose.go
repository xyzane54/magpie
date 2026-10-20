package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/yetone/magpie/internal/routeracceptance"
)

func runDiagnoseLocal(ctx context.Context, args []string, out io.Writer) error {
	options, err := routeracceptance.ParseOptions(args, out)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	report, runErr := routeracceptance.Run(ctx, options)
	writeErr := routeracceptance.WriteReport(options.Output, report, runErr)
	if writeErr == nil {
		fmt.Fprintf(out, "Local diagnostic evidence: %s\n", options.Output)
	}
	return errors.Join(runErr, writeErr)
}
