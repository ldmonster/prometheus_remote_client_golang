// Copyright (c) 2019 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	stdlog "log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ldmonster/prometheus_remote_client_golang/promremote"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type labelList []promremote.Label
type headerList []header
type dp promremote.Datapoint

type header struct {
	name  string
	value string
}

func main() {
	var (
		log            = stdlog.New(os.Stderr, "promremotecli_log ", stdlog.LstdFlags)
		writeURLFlag   string
		labelsListFlag labelList
		headerListFlag headerList
		dpFlag         dp
	)

	flag.StringVar(&writeURLFlag, "u", promremote.DefaultRemoteWrite, "remote write endpoint")
	flag.Var(&labelsListFlag, "t", "label pair to include in metric. specify as key:value e.g. status_code:200")
	flag.Var(&headerListFlag, "h", "headers to set in the request, e.g. 'User-Agent: foo'")
	flag.Var(&dpFlag, "d", "datapoint to add. specify as unixTimestamp(int),value(float) e.g. 1556026059,14.23. use `now` instead of timestamp for current time")

	flag.Parse()

	reg := prometheus.NewRegistry()

	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "some_metric_name",
		Help: "Some metric help",
	}, []string{"first", "second", "third"})
	reg.Register(cv)

	scv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "second_metric_name",
		Help: "Second metric help",
	}, []string{"one", "two", "three"})
	reg.Register(scv)

	tsList := promremote.TSList{
		{
			Labels:    []promremote.Label(labelsListFlag),
			Datapoint: promremote.Datapoint(dpFlag),
		},
	}

	cv.WithLabelValues("first", "second", "third").Inc()
	cv.WithLabelValues("fast", "second", "third").Inc()
	cv.WithLabelValues("first", "second", "third").Inc()

	scv.WithLabelValues("one", "two", "three").Inc()
	scv.WithLabelValues("fast", "two", "three").Inc()
	scv.WithLabelValues("one", "two", "three").Inc()

	mf, done, err := prometheus.ToTransactionalGatherer(reg).Gather()
	defer done()
	if err != nil {
		log.Fatal(fmt.Errorf("unable to gather metrics: %v", err))
	}

	tss := MetricFamiliesToTimeSeries(mf)

	for k, v := range tss {
		log.Println("metric name", k)
		for _, ts := range v {
			log.Println("labels", ts.Labels)
			log.Println("datapoint", ts.Datapoint)
		}
	}

	cfg := promremote.NewConfig(
		promremote.WriteURLOption(writeURLFlag),
	)

	client, err := promremote.NewClient(cfg)
	if err != nil {
		log.Fatal(fmt.Errorf("unable to construct client: %v", err))
	}

	var headers map[string]string
	log.Println("writing datapoint", dpFlag.String())
	log.Println("labelled", labelsListFlag.String())
	if len(headerListFlag) > 0 {
		log.Println("with headers", headerListFlag.String())
		headers = make(map[string]string, len(headerListFlag))
		for _, header := range headerListFlag {
			headers[header.name] = header.value
		}
	}
	log.Println("writing to", writeURLFlag)

	result, writeErr := client.WriteTimeSeries(context.Background(), tsList,
		promremote.WriteOptions{Headers: headers})
	if err := error(writeErr); err != nil {
		json.NewEncoder(os.Stdout).Encode(struct {
			Success    bool   `json:"success"`
			Error      string `json:"error"`
			StatusCode int    `json:"statusCode"`
		}{
			Success:    false,
			Error:      err.Error(),
			StatusCode: writeErr.StatusCode(),
		})
		os.Stdout.Sync()

		log.Fatal("write error", err)
	}

	json.NewEncoder(os.Stdout).Encode(struct {
		Success    bool `json:"success"`
		StatusCode int  `json:"statusCode"`
	}{
		Success:    true,
		StatusCode: result.StatusCode,
	})
	os.Stdout.Sync()

	log.Println("write success")
}

func (t *labelList) String() string {
	var labels [][]string
	for _, v := range []promremote.Label(*t) {
		labels = append(labels, []string{v.Name, v.Value})
	}
	return fmt.Sprintf("%v", labels)
}

func (t *labelList) Set(value string) error {
	labelPair := strings.Split(value, ":")
	if len(labelPair) != 2 {
		return fmt.Errorf("incorrect number of arguments to '-t': %d", len(labelPair))
	}

	label := promremote.Label{
		Name:  labelPair[0],
		Value: labelPair[1],
	}

	*t = append(*t, label)

	return nil
}

func (h *headerList) String() string {
	var headers [][]string
	for _, v := range []header(*h) {
		headers = append(headers, []string{v.name, v.value})
	}
	return fmt.Sprintf("%v", headers)
}

func (h *headerList) Set(value string) error {
	firstSplit := strings.Index(value, ":")
	if firstSplit == -1 {
		return fmt.Errorf("header missing separating colon: '%v'", value)
	}

	*h = append(*h, header{
		name:  strings.TrimSpace(value[:firstSplit]),
		value: strings.TrimSpace(value[firstSplit+1:]),
	})

	return nil
}

func (d *dp) String() string {
	return fmt.Sprintf("%v", []string{d.Timestamp.String(), fmt.Sprintf("%v", d.Value)})
}

func (d *dp) Set(value string) error {
	dp := strings.Split(value, ",")
	if len(dp) != 2 {
		return fmt.Errorf("incorrect number of arguments to '-d': %d", len(dp))
	}

	var ts time.Time
	if strings.ToLower(dp[0]) == "now" {
		ts = time.Now()
	} else {
		i, err := strconv.Atoi(dp[0])
		if err != nil {
			return fmt.Errorf("unable to parse timestamp: %s", dp[1])
		}
		ts = time.Unix(int64(i), 0)
	}

	val, err := strconv.ParseFloat(dp[1], 64)
	if err != nil {
		return fmt.Errorf("unable to parse value as float64: %s", dp[0])
	}

	d.Timestamp = ts
	d.Value = val

	return nil
}

// MetricFamiliesToTimeSeries converts Prometheus metric families to a map of promremote.TimeSeries
// where the key is the metric name and the value is a slice of time series for that metric
func MetricFamiliesToTimeSeries(
	metricFamilies []*dto.MetricFamily,
) map[string][]promremote.TimeSeries {
	result := make(map[string][]promremote.TimeSeries)

	for _, metricFamily := range metricFamilies {
		metricName := metricFamily.GetName()
		series := make([]promremote.TimeSeries, 0, len(metricFamily.Metric))

		for _, metric := range metricFamily.Metric {
			// Create labels from the metric's label pairs
			labels := make([]promremote.Label, 0, len(metric.Label)+1) // +1 for the name label
			for _, labelPair := range metric.Label {
				labels = append(labels, promremote.Label{
					Name:  labelPair.GetName(),
					Value: labelPair.GetValue(),
				})
			}

			// Add metric name as a label
			labels = append(labels, promremote.Label{
				Name:  "__name__",
				Value: metricName,
			})

			value := 0.0

			// Extract value based on metric type
			switch {
			case metric.GetCounter() != nil:
				value = metric.GetCounter().GetValue()
			case metric.GetGauge() != nil:
				value = metric.GetGauge().GetValue()
			case metric.GetHistogram() != nil:
				value = metric.GetHistogram().GetSampleSum()
				// You might want to handle histogram buckets separately here
			case metric.GetSummary() != nil:
				value = metric.GetSummary().GetSampleSum()
				// You might want to handle summary quantiles separately here
			}

			// Create and add the time series
			series = append(series, promremote.TimeSeries{
				Labels: labels,
				Datapoint: promremote.Datapoint{
					Timestamp: time.Unix(0, metric.GetTimestampMs()*int64(time.Millisecond)),
					Value:     value,
				},
			})
		}

		result[metricName] = series
	}

	return result
}

// FlattenTimeSeriesMap converts the map of time series to a flat slice
func FlattenTimeSeriesMap(timeSeriesMap map[string][]promremote.TimeSeries) []promremote.TimeSeries {
	var result []promremote.TimeSeries

	for _, series := range timeSeriesMap {
		result = append(result, series...)
	}

	return result
}
