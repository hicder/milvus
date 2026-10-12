package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/milvus-io/milvus/client/v2/entity"
)

func TestAdsSearchRequestAndHNSWSettings(t *testing.T) {
	c, err := readConfig("config-ads.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.TopK != 1000 || c.NProbe != 8 || len(c.FlightIDs) != 64 {
		t.Fatalf("unexpected ads profile: %+v", c)
	}
	c.Rows, c.Queries = 1000, 1
	d := generate(c)
	cs := cases(c)[1]
	request, err := searchOption(d, cs, 0).Request()
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{}
	for _, p := range request.SearchParams {
		params[p.Key] = p.Value
	}
	var ann map[string]int
	if err := json.Unmarshal([]byte(params["params"]), &ann); err != nil {
		t.Fatal(err)
	}
	if params["topk"] != "1000" || ann["nprobe"] != 8 || params["ignore_growing"] != "true" || entity.ConsistencyLevel(request.ConsistencyLevel) != entity.ClEventually || request.UseDefaultConsistency {
		t.Fatalf("unexpected outgoing ads request: %v", request)
	}
	if request.Dsl != cs.Filter || !reflect.DeepEqual(request.OutputFields, []string{"id", "flight_id"}) || len(d.Q100[0]) != 100 {
		t.Fatalf("ads filter, outputs, or vector shape mismatch: %v", request)
	}

	c.Workflow, c.TopK = "hnsw", 10
	d = generate(c)
	request, err = searchOption(d, cases(c)[0], 0).Request()
	if err != nil {
		t.Fatal(err)
	}
	if entity.ConsistencyLevel(request.ConsistencyLevel) != entity.ClStrong || !reflect.DeepEqual(request.OutputFields, []string{"id"}) {
		t.Fatalf("HNSW settings changed: %v", request)
	}
}

func TestEfValidationIsSpecificToHNSW(t *testing.T) {
	c := defaultConfig()
	c.Workflow, c.TopK = "ivf", 1000
	if err := c.validate(); err != nil {
		t.Fatalf("IVF must not require ef >= top_k: %v", err)
	}
	for _, workflow := range []string{"hnsw", "prq"} {
		c.Workflow = workflow
		if c.validate() == nil {
			t.Fatalf("%s accepted ef below top_k", workflow)
		}
	}
}
