package bull

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

func (b *bullServer) search(w http.ResponseWriter, r *http.Request) error {
	// TODO: implement server-rendered version for non-javascript,
	// i.e. POST /_bull/search handler
	const pageName = bullPrefix + "search"
	return b.executeTemplate(w, "search.html.tmpl", struct {
		Title       string
		RequestPath string
		Page        *page
		ReadOnly    bool
	}{
		Title:       "search",
		RequestPath: r.URL.EscapedPath(),
		Page: &page{
			PageName: pageName,
			FileName: page2desired(pageName),
		},
	})
}

func grep(content, query string) []string {
	queryl := strings.ToLower(query)
	var matches []string
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(strings.ToLower(line), queryl) {
			matches = append(matches, line)
		}
	}
	return matches
}

type progressUpdate struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type match struct {
	Type          string   `json:"type"`
	PageName      string   `json:"page_name"`
	MatchingLines []string `json:"matching_lines"`
	nameMatch     bool
}

func (b *bullServer) searchAPI(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("BUG: ResponseWriter does not implement http.Flusher")
	}

	query := r.FormValue("q")
	start := time.Now()
	log.Printf("searching for query %q", query)

	ctx := r.Context()

	initEventStream(w)

	i := newIndexer(b.content)

	var (
		resultsMu sync.Mutex
		results   []match

		readg     errgroup.Group
		progressg sync.WaitGroup

		filesRead atomic.Uint64
	)
	progressg.Add(1)
	progressCtx, progressCanc := context.WithCancel(ctx)
	defer progressCanc()
	go func() {
		defer progressg.Done()
		for {
			select {
			case <-progressCtx.Done():
				return
			case <-time.After(1 * time.Second):
				b, err := json.Marshal(progressUpdate{
					Type:    "progress",
					Message: fmt.Sprintf("searched through %d files", filesRead.Load()),
				})
				if err != nil {
					log.Print(err)
					return
				}
				if err := ctx.Err(); err != nil {
					return
				}
				w.Write(append(append([]byte("data: "), b...), '\n', '\n'))
				flusher.Flush()
			}
		}
	}()
	for range runtime.NumCPU() {
		readg.Go(func() error {
			for pg := range i.readq {
				if err := ctx.Err(); err != nil {
					return err
				}
				// fmt.Printf("reading %s\n", pg.FileName)
				pg, err := b.read(pg.FileName)
				if err != nil {
					// TODO: send an error result
					log.Printf("read: %v", err)
					continue
				}
				filesRead.Add(1)
				matches := grep(pg.Content, query)
				nameMatches := grep(pg.PageName, query)
				matches = append(matches, nameMatches...)
				if len(matches) == 0 {
					continue
				}
				m := match{
					Type:          "result",
					PageName:      pg.PageName,
					MatchingLines: matches,
					nameMatch:     len(nameMatches) > 0,
				}
				resultsMu.Lock()
				results = append(results, m)
				resultsMu.Unlock()
			}
			return nil
		})
	}
	if err := i.walk(); err != nil {
		return err
	}
	if err := readg.Wait(); err != nil {
		return err
	}
	// Synchronize with the progress update goroutine to ensure it no longer
	// tries to use the ResponseWriter by the time this handler returns.
	progressCanc()
	progressg.Wait()

	sort.Slice(results, func(i, j int) bool {
		return results[i].PageName < results[j].PageName
	})

	sort.SliceStable(results, func(i, j int) bool {
		ri := results[i]
		rj := results[j]
		if ri.nameMatch && !rj.nameMatch {
			return true
		}
		if !ri.nameMatch && rj.nameMatch {
			return false
		}
		// both are a page name match

		// TODO: rank by some other criterion

		return false
	})

	log.Printf("search for query %q done in %v, now streaming results", query, time.Since(start))

	// stream search results
	for _, result := range results {
		b, err := json.Marshal(result)
		if err != nil {
			return err
		}
		w.Write(append(append([]byte("data: "), b...), '\n', '\n'))
	}

	w.Write([]byte(`data: {"type":"done"}` + "\n\n"))
	flusher.Flush()
	return nil
}
