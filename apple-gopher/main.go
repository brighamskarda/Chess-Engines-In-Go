// Copyright (C) 2026 Brigham Skarda
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"

	"github.com/brighamskarda/chess/v2/uci"
)

var logLevels map[string]slog.Level = map[string]slog.Level{
	"DEBUG": slog.LevelDebug,
	"INFO":  slog.LevelInfo,
	"WARN":  slog.LevelWarn,
	"ERROR": slog.LevelError,
}

func main() {
	myEngine := &engine{}

	// command line flags
	logFile := flag.String("logFile", "apple-gopher.log", "the engine log file location")
	logLevel := flag.String("logLevel", "INFO",
		fmt.Sprintf("the logging level used for the program %v", slices.Collect(maps.Keys(logLevels))))
	flag.Parse()

	logger := setupLogging(*logFile, *logLevel)

	broker := uci.UciEngineBroker{
		Engine: myEngine,
		Input:  os.Stdin,
		Output: os.Stdout,
		Log:    logger,
	}

	broker.Start(context.Background())

}

func setupLogging(logFile string, logLevel string) *slog.Logger {
	file, err := os.Create(logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not open log file: %v", err)
		os.Exit(1)
	}

	level, ok := logLevels[logLevel]
	if !ok {
		fmt.Fprintf(os.Stderr, "invalid log level %q: expected one of %v", logLevel, slices.Collect(maps.Keys(logLevels)))
		os.Exit(1)
	}

	return slog.New(slog.NewTextHandler(file, &slog.HandlerOptions{Level: level}))
}
