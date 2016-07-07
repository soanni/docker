package service

import (
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/context"

	"github.com/docker/docker/api/client"
	"github.com/docker/docker/api/client/inspect"
	"github.com/docker/docker/cli"
	"github.com/docker/docker/pkg/ioutils"
	apiclient "github.com/docker/engine-api/client"
	"github.com/docker/engine-api/types/swarm"
	"github.com/docker/go-units"
	"github.com/spf13/cobra"
)

type inspectOptions struct {
	refs   []string
	format string
	pretty bool
}

func newInspectCommand(dockerCli *client.DockerCli) *cobra.Command {
	var opts inspectOptions

	cmd := &cobra.Command{
		Use:   "inspect [OPTIONS] SERVICE [SERVICE...]",
		Short: "Display detailed information on one or more services",
		Args:  cli.RequiresMinArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.refs = args

			if opts.pretty && len(opts.format) > 0 {
				return fmt.Errorf("--format is incompatible with human friendly format")
			}
			return runInspect(dockerCli, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&opts.format, "format", "f", "", "Format the output using the given go template")
	flags.BoolVarP(&opts.pretty, "pretty", "p", false, "Print the information in a human friendly format.")
	return cmd
}

func runInspect(dockerCli *client.DockerCli, opts inspectOptions) error {
	client := dockerCli.Client()
	ctx := context.Background()

	getRef := func(ref string) (interface{}, []byte, error) {
		service, _, err := client.ServiceInspectWithRaw(ctx, ref)
		if err == nil || !apiclient.IsErrServiceNotFound(err) {
			return service, nil, err
		}
		return nil, nil, fmt.Errorf("Error: no such service: %s", ref)
	}

	format := opts.format
	if opts.pretty {
		format = `ID:		{{.ID}}
Name:		{{.Spec.Name}}{{if .Spec.Labels}}
Labels:
{{range $key, $value := .Spec.Labels}}
 - {{$key}}${{$value}}
{{end}}{{end}}{{if .Spec.Mode.Global}}
Mode:		Global
{{else}}
Mode:		Replicated{{end}}
Placement:
 Strategy:	Spread{{if .Spec.TaskTemplate.Placement}}{{ if .Spec.TaskTemplate.Placement.Constraints}} Constraints	{{range $index, $element := .Spec.TaskTemplate.Placement.Constraints}}{{if $index}}, {{end}}{{$element}}{{end}}{{end}}{{end}}
UpdateConfig:
 Parallelism:	{{.Spec.UpdateConfig.Delay}}{{ if .Spec.UpdateConfig.Delay.Nanoseconds }}
 Delay:		{{.Spec.UpdateConfig.Delay}}{{end}}
{{$containerSpec := .Spec.TaskTemplate.ContainerSpec}}ContainerSpec:
 Image:		{{$containerSpec.Image}}{{if $containerSpec.Command}}
 Command:	{{ range $index, $element := $containerSpec.Command}}{{if $index}} {{end}}{{$element}}{{end}}{{end}}{{if $containerSpec.Args}}
 Args:		{{ range $index, $element := $containerSpec.Args}}{{if $index}} {{end}}{{$element}}{{end}}{{end}}{{if $containerSpec.Env}}
 Env:		{{ range $index, $element := $containerSpec.Env}}{{if $index}} {{end}}{{$element}}{{end}}{{end}}{{if $containerSpec.Dir}}
 Dir:		{{$containerSpec.Dir}}{{end}}{{if $containerSpec.User}}
 User:		{{end}}{{if $containerSpec.Mounts}}
 Mounts:{{range $containerSpec.Mounts}}
  Target = {{.Target}}
  Source = {{.Source}}
  ReadOnly = {{.ReadOnly}}
  Type = {{.Type}}
{{end}}{{end}}{{if .Spec.TaskTemplate.Resources}}
Resources:{{if .Spec.TaskTemplate.Resources.Reservations}}
Reservations:{{if .Spec.TaskTemplate.Resources.Reservations.NanoCPUs}}
 CPU:		{{.Spec.TaskTemplate.Resources.Reservations.NanoCPUs}}{{end}}{{if .Spec.TaskTemplate.Resources.Reservations.MemoryBytes}}
 Memory:		{{.Spec.TaskTemplate.Resources.Reservations.MemoryBytes}}{{end}}
{{end}}{{if .Spec.TaskTemplate.Resources.Limits}}
Limits:{{if .Spec.TaskTemplate.Resources.Limits.NanoCPUs}}
 CPU:		{{.Spec.TaskTemplate.Resources.Limits.NanoCPUs}}{{end}}{{if .Spec.TaskTemplate.Resources.Limits.MemoryBytes}}
 Memory:		{{.Spec.TaskTemplate.Resources.Limits.MemoryBytes}}{{end}}
{{end}}{{end}}
`
	}
	return inspect.Inspect(dockerCli.Out(), opts.refs, format, getRef)
	// return printHumanFriendly(dockerCli.Out(), opts.refs, getRef)
}

// TODO: use a template
func printService(out io.Writer, service swarm.Service) {
	if len(service.Spec.Networks) > 0 {
		fmt.Fprintf(out, "Networks:")
		for _, n := range service.Spec.Networks {
			fmt.Fprintf(out, " %s", n.Target)
		}
	}

	if len(service.Endpoint.Ports) > 0 {
		fmt.Fprintln(out, "Ports:")
		for _, port := range service.Endpoint.Ports {
			fmt.Fprintf(out, " Name = %s\n", port.Name)
			fmt.Fprintf(out, " Protocol = %s\n", port.Protocol)
			fmt.Fprintf(out, " TargetPort = %d\n", port.TargetPort)
			fmt.Fprintf(out, " PublishedPort = %d\n", port.PublishedPort)
		}
	}
}

func printHumanFriendly(out io.Writer, refs []string, getRef inspect.GetRefFunc) error {
	for idx, ref := range refs {
		obj, _, err := getRef(ref)
		if err != nil {
			return err
		}
		printService(out, obj.(swarm.Service))

		// TODO: better way to do this?
		// print extra space between objects, but not after the last one
		if idx+1 != len(refs) {
			fmt.Fprintf(out, "\n\n")
		}
	}
	return nil
}
