package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

func mediaFolderColumns() []output.Column {
	return []output.Column{
		{Header: "uuid", Path: "uuid"},
		{Header: "name", Path: "name", MaxWidth: 34},
		{Header: "parent", Path: "parent"},
		{Header: "files", Path: "media_count"},
	}
}

func newMediaFolderCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "folder",
		Aliases: []string{"folders"},
		Short:   "Organise the media library into folders",
		Long: `Manage media folders.

A folder is a label rather than a place: filing a file under one never moves
the file, and its URL stays the same. Folders nest up to five levels deep.

Wherever a folder is expected it can be named by uuid, by name when the name is
unique, or by its path from the root, such as Brand/Q4.`,
	}
	cmd.AddCommand(
		newMediaFolderListCommand(),
		newMediaFolderCreateCommand(),
		newMediaFolderUpdateCommand(),
		newMediaFolderDeleteCommand(),
	)
	return cmd
}

func newMediaFolderListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List every folder, with how many files sit at or below it",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := env.Client.Get(ctxOf(cmd), "media/folders", nil)
			if err != nil {
				return err
			}
			return env.Render(resp, mediaFolderColumns())
		},
	}
}

func newMediaFolderCreateCommand() *cobra.Command {
	var parent string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a folder, at the root or inside another",
		Example: `  mixpost media folder create Brand
  mixpost media folder create Q4 --parent Brand`,
		Args: exactArgs(1, "mixpost media folder create <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"name": args[0]}
			if parent != "" {
				uuid, root, err := resolveFolder(ctxOf(cmd), env.Client, parent)
				if err != nil {
					return err
				}
				if !root {
					body["parent"] = uuid
				}
			}
			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPost, "media/folders", nil, body)
			if err != nil {
				return err
			}
			return env.Render(resp, mediaFolderColumns())
		},
	}
	cmd.Flags().StringVar(&parent, "parent", "", "folder to create it in (default: the root)")
	return cmd
}

func newMediaFolderUpdateCommand() *cobra.Command {
	var name, parent string

	cmd := &cobra.Command{
		Use:   "update <folder>",
		Short: "Rename a folder or move it under another",
		Example: `  mixpost media folder update Brand --name "Brand 2026"
  mixpost media folder update Brand/Q4 --parent root`,
		Args: exactArgs(1, "mixpost media folder update <folder> --name <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if cmd.Flags().Changed("name") {
				body["name"] = name
			}
			if cmd.Flags().Changed("parent") {
				uuid, root, err := resolveFolder(ctxOf(cmd), env.Client, parent)
				if err != nil {
					return err
				}
				if root {
					body["parent"] = nil
				} else {
					body["parent"] = uuid
				}
			}
			if len(body) == 0 {
				return usageError(errors.New("nothing to change: pass --name or --parent"))
			}

			target, root, err := resolveFolder(ctxOf(cmd), env.Client, args[0])
			if err != nil {
				return err
			}
			if root {
				return usageError(errors.New("the root is not a folder that can be changed"))
			}
			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPut, "media/folders/"+target, nil, body)
			if err != nil {
				return err
			}
			return env.Render(resp, mediaFolderColumns())
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new folder name")
	cmd.Flags().StringVar(&parent, "parent", "", `folder to move it under, or "root"`)
	return cmd
}

func newMediaFolderDeleteCommand() *cobra.Command {
	var deleteMedia bool

	cmd := &cobra.Command{
		Use:     "delete <folder>...",
		Aliases: []string{"rm"},
		Short:   "Delete folders, keeping their files unless --delete-media",
		Long: `Delete folders.

The files and subfolders a folder held move up to where it sat. --delete-media
deletes every file at or below the folder instead, detaching each from the
posts that used it.`,
		Example: `  mixpost media folder delete Brand/Q4
  mixpost media folder delete Drafts --delete-media --yes`,
		Args: minArgs(1, "mixpost media folder delete <folder>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets := make([]string, 0, len(args))
			for _, arg := range args {
				uuid, root, err := resolveFolder(ctxOf(cmd), env.Client, arg)
				if err != nil {
					return err
				}
				if root {
					return usageError(errors.New("the root is not a folder that can be deleted"))
				}
				targets = append(targets, uuid)
			}

			prompt := pluralPrompt("Delete", len(targets), "folder")
			if deleteMedia {
				prompt = strings.TrimSuffix(prompt, "?") + " and every file in them?"
			}
			if err := env.Confirm(prompt); err != nil {
				return err
			}

			for _, uuid := range targets {
				resp, err := env.Client.Call(ctxOf(cmd), http.MethodDelete, "media/folders/"+uuid, nil, map[string]any{
					"delete_media": deleteMedia,
				})
				if err != nil {
					return err
				}
				if err := env.Render(resp, nil); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteMedia, "delete-media", false, "delete the files in the folder as well")
	return cmd
}

// resolveFolder turns a folder named on the command line into its uuid. A uuid
// is taken as given; "root" (or "/") reports root; anything else is matched
// against the folder tree, by path from the root and then by name.
func resolveFolder(ctx context.Context, client *mixpost.Client, spec string) (uuid string, root bool, err error) {
	spec = strings.TrimSpace(spec)
	switch {
	case strings.EqualFold(spec, "root") || spec == "/":
		return "", true, nil
	case mixpost.LooksLikeUUID(spec):
		return spec, false, nil
	}

	folders, err := client.MediaFolders(ctx)
	if err != nil {
		return "", false, fmt.Errorf("look up media folders to resolve %q: %w", spec, err)
	}
	paths := folderPaths(folders)

	wanted := strings.Trim(spec, "/")
	for _, folder := range folders {
		if strings.EqualFold(paths[folder.UUID], wanted) {
			return folder.UUID, false, nil
		}
	}

	var matches []string
	var found string
	for _, folder := range folders {
		if strings.EqualFold(folder.Name, wanted) {
			matches = append(matches, paths[folder.UUID])
			found = folder.UUID
		}
	}
	switch len(matches) {
	case 1:
		return found, false, nil
	case 0:
		return "", false, usageError(fmt.Errorf("no media folder matches %q; available: %s", spec, describeFolders(paths)))
	}
	sort.Strings(matches)
	return "", false, usageError(fmt.Errorf("%q matches %d folders (%s); name it by its path or uuid", spec, len(matches), strings.Join(matches, ", ")))
}

// folderPaths spells each folder's place in the tree as Parent/Child.
func folderPaths(folders []mixpost.MediaFolder) map[string]string {
	byUUID := make(map[string]mixpost.MediaFolder, len(folders))
	for _, folder := range folders {
		byUUID[folder.UUID] = folder
	}

	paths := make(map[string]string, len(folders))
	for _, folder := range folders {
		parts := []string{folder.Name}
		seen := map[string]bool{folder.UUID: true}
		for parent := folder.Parent; parent != "" && !seen[parent]; {
			seen[parent] = true
			ancestor, ok := byUUID[parent]
			if !ok {
				break
			}
			parts = append([]string{ancestor.Name}, parts...)
			parent = ancestor.Parent
		}
		paths[folder.UUID] = strings.Join(parts, "/")
	}
	return paths
}

func describeFolders(paths map[string]string) string {
	if len(paths) == 0 {
		return "none"
	}
	list := make([]string, 0, len(paths))
	for _, path := range paths {
		list = append(list, path)
	}
	sort.Strings(list)
	return strings.Join(list, ", ")
}
