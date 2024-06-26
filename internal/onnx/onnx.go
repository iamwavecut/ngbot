// Copyright 2022 The NLP Odyssey Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nlpodyssey/cybertron/pkg/tasks"
	"github.com/nlpodyssey/cybertron/pkg/tasks/zeroshotclassifier"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	modelsDir := "models"
	modelName := "MoritzLaurer/mDeBERTa-v3-base-mnli-xnli"

	m, err := tasks.Load[zeroshotclassifier.Interface](&tasks.Config{
		ModelsDir:           modelsDir,
		ModelName:           modelName,
		DownloadPolicy:      tasks.DownloadMissing,
		ConversionPolicy:    tasks.ConvertMissing,
		ConversionPrecision: tasks.F32,
	})
	if err != nil {
		log.Fatal().Err(err).Send()
	}

	possibleClasses := []string{
		"предложение работы",
		"предложение сотрудничества",
		"поиск бизнесс партнера",
		"поиск сотрудников",
		"другое",
	}
	params := zeroshotclassifier.Parameters{
		CandidateLabels:    possibleClasses,
		HypothesisTemplate: "{}",
		MultiLabel:         false,
	}

	fn := func(text string) error {
		start := time.Now()
		result, err := m.Classify(context.Background(), text, params)
		if err != nil {
			return err
		}
		fmt.Println(time.Since(start).Seconds())

		for i := range result.Labels {
			fmt.Printf("%s\t%0.3f\n", result.Labels[i], result.Scores[i])
		}
		return nil
	}

	err = ForEachInput(os.Stdin, fn)
	if err != nil {
		log.Fatal().Err(err).Send()
	}
}

// ForEachInput calls the given callback function for each line of input.
func ForEachInput(r io.Reader, callback func(text string) error) error {
	scanner := bufio.NewScanner(r)
	for {
		fmt.Print("> ")
		scanner.Scan()
		text := scanner.Text()
		if text == "" {
			break
		}
		if err := callback(text); err != nil {
			return err
		}
	}
	return nil
}
