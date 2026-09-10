package cli

import (
	"fmt"
	"strings"

	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/spf13/cobra"
)

var rawMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}

func newRawCommand() *cobra.Command {
	var (
		data        string
		file        string
		headers     []string
		queryParams []string
		noWorkspace bool
		paginate    bool
		limit       int
	)

	cmd := &cobra.Command{
		Use:   "raw <method> <path>",
		Short: "Call any API endpoint directly",
		Long: `Call an endpoint the CLI does not wrap.

The path is relative to /api and is prefixed with the current workspace uuid, so
"posts" becomes /api/<workspace>/posts. Instance-level paths such as "ping" and
"workspaces" are recognised and left unprefixed; --no-workspace forces that for
any other path.`,
		Example: `  mixpost raw GET posts -q page=2
  mixpost raw GET ping
  mixpost raw POST tags -d '{"name":"launch","hex_color":"#60a5fa"}'
  mixpost raw PUT posts/8eb4e98d-... -f body.json
  mixpost raw GET posts --paginate --limit 100`,
		Args: exactArgs(2, "mixpost raw GET posts"),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			if !contains(rawMethods, method) {
				return usageError(fmt.Errorf("unsupported method %q (want one of: %s)", args[0], strings.Join(rawMethods, ", ")))
			}
			path := strings.TrimPrefix(args[1], "/")

			query := queryValues{}
			for _, pair := range queryParams {
				key, value, err := parseKeyValue(pair, "query")
				if err != nil {
					return err
				}
				query.Add(key, value)
			}

			var body []byte
			switch {
			case data != "" && file != "":
				return usageError(fmt.Errorf("pass either --data or --file, not both"))
			case file != "":
				loaded, err := readInput(file)
				if err != nil {
					return err
				}
				body = loaded
			case data != "":
				body = []byte(data)
			}

			extraHeaders := map[string]string{}
			for _, pair := range headers {
				key, value, found := strings.Cut(pair, ":")
				if !found {
					return usageError(fmt.Errorf("--header expects 'Name: value', got %q", pair))
				}
				extraHeaders[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}

			if paginate || limit > 0 {
				if method != "GET" {
					return usageError(fmt.Errorf("--paginate only applies to GET"))
				}
				raw, err := env.Client.Paginate(ctxOf(cmd), path, query, limit)
				if err != nil {
					return err
				}
				return env.RenderRaw(raw, nil)
			}

			resp, err := env.Client.Do(ctxOf(cmd), mixpost.Request{
				Method:   method,
				Path:     path,
				Instance: noWorkspace,
				Query:    query,
				RawBody:  body,
				Headers:  extraHeaders,
			})
			if err != nil {
				return err
			}
			return env.Render(resp, nil)
		},
	}

	f := cmd.Flags()
	f.StringVarP(&data, "data", "d", "", "request body")
	f.StringVarP(&file, "file", "f", "", `read the request body from a file ("-" reads stdin)`)
	f.StringArrayVarP(&headers, "header", "H", nil, "extra header as 'Name: value'; repeatable")
	f.StringArrayVarP(&queryParams, "query", "q", nil, "query parameter as key=value; repeatable")
	f.BoolVar(&noWorkspace, "no-workspace", false, "do not prefix the path with the workspace uuid")
	f.BoolVar(&paginate, "paginate", false, "follow links.next and merge every page")
	f.IntVar(&limit, "limit", 0, "stop after this many items (implies --paginate)")

	cmd.ValidArgsFunction = func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return rawMethods, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}
