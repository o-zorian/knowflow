package chat

import "testing"

func TestCitationFilterValidatesSplitMarkers(t *testing.T) {
	filter := NewCitationFilter(2)
	var output string
	for _, delta := range []string{"事实 [", "1]，无效 [9]", "，文本 [x] 和 [", "2]。"} {
		output += filter.Feed(delta)
	}
	output += filter.Close()
	if output != "事实 [1]，无效 ，文本 [x] 和 [2]。" {
		t.Fatalf("filtered output = %q", output)
	}
	numbers := filter.Numbers()
	if len(numbers) != 2 || numbers[0] != 1 || numbers[1] != 2 {
		t.Fatalf("citation numbers = %#v", numbers)
	}
}

func TestCitationFilterDeduplicatesNumbers(t *testing.T) {
	filter := NewCitationFilter(1)
	if got := filter.Feed("[1] again [1]") + filter.Close(); got != "[1] again [1]" {
		t.Fatalf("output = %q", got)
	}
	if numbers := filter.Numbers(); len(numbers) != 1 || numbers[0] != 1 {
		t.Fatalf("citation numbers = %#v", numbers)
	}
}
