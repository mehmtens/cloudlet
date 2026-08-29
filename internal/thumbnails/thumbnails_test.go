package thumbnails

import "testing"

func TestFitPreservesAspectRatio(t *testing.T) {
	tests := []struct{ width, height, wantWidth, wantHeight int }{
		{640, 480, 320, 240},
		{1000, 500, 320, 160},
		{500, 1000, 120, 240},
		{100, 80, 100, 80},
	}
	for _, test := range tests {
		width, height := fit(test.width, test.height, 320, 240)
		if width != test.wantWidth || height != test.wantHeight {
			t.Fatalf("fit(%d,%d)=(%d,%d), want (%d,%d)", test.width, test.height, width, height, test.wantWidth, test.wantHeight)
		}
	}
}
