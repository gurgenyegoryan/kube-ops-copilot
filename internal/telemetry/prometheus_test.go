package telemetry

import "testing"

func TestNormalizeNamespacePodLabels(t *testing.T) {
	cases := []struct {
		name    string
		metric  map[string]string
		wantNS  string
		wantPod string
	}{
		{
			name: "canonical",
			metric: map[string]string{
				"namespace": "prod",
				"pod":       "api-123",
			},
			wantNS:  "prod",
			wantPod: "api-123",
		},
		{
			name: "namespace_name_pod_name",
			metric: map[string]string{
				"namespace_name": "prod",
				"pod_name":       "api-456",
			},
			wantNS:  "prod",
			wantPod: "api-456",
		},
		{
			name: "exported aliases",
			metric: map[string]string{
				"exported_namespace": "prod",
				"exported_pod":       "api-789",
			},
			wantNS:  "prod",
			wantPod: "api-789",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotNS, gotPod := NormalizeNamespacePodLabels(tc.metric)
			if gotNS != tc.wantNS || gotPod != tc.wantPod {
				t.Fatalf("unexpected normalized labels: ns=%q pod=%q", gotNS, gotPod)
			}
		})
	}
}

func TestDetectNamespacePodLabelPair(t *testing.T) {
	ns, pod := DetectNamespacePodLabelPair([]string{"cluster", "exported_namespace", "exported_pod", "container"})
	if ns != "exported_namespace" || pod != "exported_pod" {
		t.Fatalf("unexpected label pair: %s %s", ns, pod)
	}
}

func TestDecodeVector(t *testing.T) {
	body := []byte(`{
	  "status": "success",
	  "data": {
	    "resultType": "vector",
	    "result": [
	      {
	        "metric": {"namespace":"prod","pod":"api-123"},
	        "value": [1710000000, "3"]
	      }
	    ]
	  }
	}`)

	samples, err := decodeVector(body)
	if err != nil {
		t.Fatalf("decodeVector returned error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].Metric["pod"] != "api-123" || samples[0].Value != 3 {
		t.Fatalf("unexpected decoded sample: %+v", samples[0])
	}
}

func TestDecodeMatrix(t *testing.T) {
	body := []byte(`{
	  "status": "success",
	  "data": {
	    "resultType": "matrix",
	    "result": [
	      {
	        "metric": {"namespace":"prod","pod":"api-123"},
	        "values": [
	          [1710000000, "0.1"],
	          [1710000300, "0.8"]
	        ]
	      }
	    ]
	  }
	}`)

	series, err := decodeMatrix(body)
	if err != nil {
		t.Fatalf("decodeMatrix returned error: %v", err)
	}
	if len(series) != 1 || len(series[0].Values) != 2 {
		t.Fatalf("unexpected decoded matrix: %+v", series)
	}
	if series[0].Values[1].Value != 0.8 {
		t.Fatalf("unexpected peak value: %+v", series[0].Values[1])
	}
}
