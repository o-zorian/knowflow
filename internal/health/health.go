package health

import (
	"context"
	"sync"
)

type Dependency struct {
	Name  string
	Check func(context.Context) error
}

func CheckAll(ctx context.Context, dependencies []Dependency) map[string]error {
	results := make(map[string]error, len(dependencies))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, dependency := range dependencies {
		dependency := dependency
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := dependency.Check(ctx)
			mu.Lock()
			results[dependency.Name] = err
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}
