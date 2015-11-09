package main

import (
	"encoding/json"
	"strings"

	"github.com/docker/docker/pkg/integration/checker"
	"github.com/go-check/check"
)

func (s *DockerSuite) TestBuildEnvironmentReplacementUser(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvironmentreplacement"

	_, err := buildImage(name, `
  FROM scratch
  ENV user foo
  USER ${user}
  `, true)
	c.Assert(err, checker.IsNil)

	res, err := inspectFieldJSON(name, "Config.User")
	c.Assert(err, checker.IsNil)

	c.Assert(res, checker.Equals, `"foo"`, check.Commentf("User foo from environment not in Config.User on image"))
}

func (s *DockerSuite) TestBuildEnvironmentReplacementVolume(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvironmentreplacement"

	_, err := buildImage(name, `
  FROM scratch
  ENV volume /quux
  VOLUME ${volume}
  `, true)
	c.Assert(err, checker.IsNil)

	res, err := inspectFieldJSON(name, "Config.Volumes")
	c.Assert(err, checker.IsNil)

	var volumes map[string]interface{}

	c.Assert(json.Unmarshal([]byte(res), &volumes), checker.IsNil)

	_, ok := volumes["/quux"]
	c.Assert(ok, checker.True, check.Commentf("Volume /quux from environment not in Config.Volumes on image"))
}

func (s *DockerSuite) TestBuildEnvironmentReplacementExpose(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvironmentreplacement"

	_, err := buildImage(name, `
  FROM scratch
  ENV port 80
  EXPOSE ${port}
  `, true)
	c.Assert(err, checker.IsNil)

	res, err := inspectFieldJSON(name, "Config.ExposedPorts")
	c.Assert(err, checker.IsNil)

	var exposedPorts map[string]interface{}

	c.Assert(json.Unmarshal([]byte(res), &exposedPorts), checker.IsNil)

	_, ok := exposedPorts["80/tcp"]
	c.Assert(ok, checker.True, check.Commentf("Exposed port 80 from environment not in Config.ExposedPorts on image"))
}

func (s *DockerSuite) TestBuildEnvironmentReplacementWorkdir(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvironmentreplacement"

	_, err := buildImage(name, `
  FROM busybox
  ENV MYWORKDIR /work
  RUN mkdir ${MYWORKDIR}
  WORKDIR ${MYWORKDIR}
  `, true)
	c.Assert(err, checker.IsNil)
}

func (s *DockerSuite) TestBuildEnvironmentReplacementAddCopy(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvironmentreplacement"

	ctx, err := fakeContext(`
  FROM scratch
  ENV baz foo
  ENV quux bar
  ENV dot .
  ENV fee fff
  ENV gee ggg

  ADD ${baz} ${dot}
  COPY ${quux} ${dot}
  ADD ${zzz:-${fee}} ${dot}
  COPY ${zzz:-${gee}} ${dot}
  `,
		map[string]string{
			"foo": "test1",
			"bar": "test2",
			"fff": "test3",
			"ggg": "test4",
		})

	c.Assert(err, checker.IsNil)
	defer ctx.Close()

	_, err := buildImageFromContext(name, ctx, true)
	c.Assert(err, checker.IsNil)
}

func (s *DockerSuite) TestBuildEnvironmentReplacementEnv(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvironmentreplacement"

	_, err := buildImage(name,
		`
  FROM busybox
  ENV foo zzz
  ENV bar ${foo}
  ENV abc1='$foo'
  ENV env1=$foo env2=${foo} env3="$foo" env4="${foo}"
  RUN [ "$abc1" = '$foo' ] && (echo "$abc1" | grep -q foo)
  ENV abc2="\$foo"
  RUN [ "$abc2" = '$foo' ] && (echo "$abc2" | grep -q foo)
  ENV abc3 '$foo'
  RUN [ "$abc3" = '$foo' ] && (echo "$abc3" | grep -q foo)
  ENV abc4 "\$foo"
  RUN [ "$abc4" = '$foo' ] && (echo "$abc4" | grep -q foo)
  `, true)
	c.Assert(err, checker.IsNil)

	res, err := inspectFieldJSON(name, "Config.Env")
	c.Assert(err, checker.Isnil)

	envResult := []string{}

	c.Assert(unmarshalJSON([]byte(res), &envResult), checker.IsNil)

	found := false
	envCount := 0

	for _, env := range envResult {
		parts := strings.SplitN(env, "=", 2)
		if parts[0] == "bar" {
			found = true
			c.Assert(parts[1], checker.Equals, "zzz",
				check.Commentf("Could not find replaced var for env `bar`"))
		} else if strings.HasPrefix(parts[0], "env") {
			envCount++
			c.Assert(parts[1], checker.Equals, "zzz")
		} else if strings.HasPrefix(parts[0], "env") {
			envCount++
			c.Assert(parts[1], checker.Equals, "foo")
		}
	}

	c.Assert(found, checker.True, check.Commentf("Never found the `bar` env variable"))
	c.Assert(envCount, checker.Equals, 4, check.Commentf("Didn't find all env vars - only saw %d", envResult))

}
func (s *DockerSuite) TestBuildEnvEscapes(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvescapes"
	_, err := buildImage(name,
		`
    FROM busybox
    ENV TEST foo
    CMD echo \$
    `,
		true)
	c.Assert(err, checker.IsNil)

	out, _ := dockerCmd(c, "run", "-t", name)

	c.Assert(strings.TrimSpace(out), checker.Equals, "$", check.Commentf("Env TEST was not overwritten with bar when foo was supplied to dockerfile"))

}

func (s *DockerSuite) TestBuildEnvOverwrite(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvoverwrite"

	_, err := buildImage(name,
		`
    FROM busybox
    ENV TEST foo
    CMD echo ${TEST}
    `,
		true)
	c.Assert(err, checker.IsNil)

	out, _ := dockerCmd(c, "run", "-e", "TEST=bar", "-t", name)

	c.Assert(strings.TrimSpace(out), checker.Equals, "bar", check.Commentf("Env TEST was not overwritten with bar when foo was supplied to dockerfile"))

}
func (s *DockerSuite) TestBuildEnv(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildenv"
	expected := "[PATH=/test:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin PORT=2375]"
	_, err := buildImage(name,
		`FROM busybox
		ENV PATH /test:$PATH
        ENV PORT 2375
		RUN [ $(env | grep PORT) = 'PORT=2375' ]`,
		true)
	if err != nil {
		c.Fatal(err)
	}
	res, err := inspectField(name, "Config.Env")
	if err != nil {
		c.Fatal(err)
	}
	if res != expected {
		c.Fatalf("Env %s, expected %s", res, expected)
	}
}
func (s *DockerSuite) TestBuildEnvUsage(c *check.C) {
	// /docker/world/hello is not owned by the correct user
	testRequires(c, NotUserNamespace)
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvusage"
	dockerfile := `FROM busybox
ENV    HOME /root
ENV    PATH $HOME/bin:$PATH
ENV    PATH /tmp:$PATH
RUN    [ "$PATH" = "/tmp:$HOME/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" ]
ENV    FOO /foo/baz
ENV    BAR /bar
ENV    BAZ $BAR
ENV    FOOPATH $PATH:$FOO
RUN    [ "$BAR" = "$BAZ" ]
RUN    [ "$FOOPATH" = "$PATH:/foo/baz" ]
ENV	   FROM hello/docker/world
ENV    TO /docker/world/hello
ADD    $FROM $TO
RUN    [ "$(cat $TO)" = "hello" ]
ENV    abc=def
ENV    ghi=$abc
RUN    [ "$ghi" = "def" ]
`
	ctx, err := fakeContext(dockerfile, map[string]string{
		"hello/docker/world": "hello",
	})
	if err != nil {
		c.Fatal(err)
	}
	defer ctx.Close()

	_, err = buildImageFromContext(name, ctx, true)
	if err != nil {
		c.Fatal(err)
	}
}

func (s *DockerSuite) TestBuildEnvUsage2(c *check.C) {
	// /docker/world/hello is not owned by the correct user
	testRequires(c, NotUserNamespace)
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvusage2"
	dockerfile := `FROM busybox
ENV    abc=def
RUN    [ "$abc" = "def" ]
ENV    def="hello world"
RUN    [ "$def" = "hello world" ]
ENV    def=hello\ world
RUN    [ "$def" = "hello world" ]
ENV    v1=abc v2="hi there"
RUN    [ "$v1" = "abc" ]
RUN    [ "$v2" = "hi there" ]
ENV    v3='boogie nights' v4="with'quotes too"
RUN    [ "$v3" = "boogie nights" ]
RUN    [ "$v4" = "with'quotes too" ]
ENV    abc=zzz FROM=hello/docker/world
ENV    abc=zzz TO=/docker/world/hello
ADD    $FROM $TO
RUN    [ "$(cat $TO)" = "hello" ]
ENV    abc "zzz"
RUN    [ $abc = "zzz" ]
ENV    abc 'yyy'
RUN    [ $abc = 'yyy' ]
ENV    abc=
RUN    [ "$abc" = "" ]

# use grep to make sure if the builder substitutes \$foo by mistake
# we don't get a false positive
ENV    abc=\$foo
RUN    [ "$abc" = "\$foo" ] && (echo "$abc" | grep foo)
ENV    abc \$foo
RUN    [ "$abc" = "\$foo" ] && (echo "$abc" | grep foo)

ENV    abc=\'foo\'
RUN    [ "$abc" = "'foo'" ]
ENV    abc=\"foo\"
RUN    [ "$abc" = "\"foo\"" ]
ENV    abc "foo"
RUN    [ "$abc" = "foo" ]
ENV    abc 'foo'
RUN    [ "$abc" = 'foo' ]
ENV    abc \'foo\'
RUN    [ "$abc" = "'foo'" ]
ENV    abc \"foo\"
RUN    [ "$abc" = '"foo"' ]

ENV    abc=ABC
RUN    [ "$abc" = "ABC" ]
ENV    def=${abc:-DEF}
RUN    [ "$def" = "ABC" ]
ENV    def=${ccc:-DEF}
RUN    [ "$def" = "DEF" ]
ENV    def=${ccc:-${def}xx}
RUN    [ "$def" = "DEFxx" ]
ENV    def=${def:+ALT}
RUN    [ "$def" = "ALT" ]
ENV    def=${def:+${abc}:}
RUN    [ "$def" = "ABC:" ]
ENV    def=${ccc:-\$abc:}
RUN    [ "$def" = '$abc:' ]
ENV    def=${ccc:-\${abc}:}
RUN    [ "$def" = '${abc:}' ]
ENV    mypath=${mypath:+$mypath:}/home
RUN    [ "$mypath" = '/home' ]
ENV    mypath=${mypath:+$mypath:}/away
RUN    [ "$mypath" = '/home:/away' ]

ENV    e1=bar
ENV    e2=$e1
ENV    e3=$e11
ENV    e4=\$e1
ENV    e5=\$e11
RUN    [ "$e0,$e1,$e2,$e3,$e4,$e5" = ',bar,bar,,$e1,$e11' ]

ENV    ee1 bar
ENV    ee2 $ee1
ENV    ee3 $ee11
ENV    ee4 \$ee1
ENV    ee5 \$ee11
RUN    [ "$ee1,$ee2,$ee3,$ee4,$ee5" = 'bar,bar,,$ee1,$ee11' ]

ENV    eee1="foo"
ENV    eee2='foo'
ENV    eee3 "foo"
ENV    eee4 'foo'
RUN    [ "$eee1,$eee2,$eee3,$eee4" = 'foo,foo,foo,foo' ]

`
	ctx, err := fakeContext(dockerfile, map[string]string{
		"hello/docker/world": "hello",
	})
	if err != nil {
		c.Fatal(err)
	}
	defer ctx.Close()

	_, err = buildImageFromContext(name, ctx, true)
	if err != nil {
		c.Fatal(err)
	}
}
