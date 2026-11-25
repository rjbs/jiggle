#!/bin/sh
# Builds or serves the Jiggle site with Zensical, run in Docker so that
# nothing need be installed locally beyond Docker and Perl.
#
#   doc/web/build.sh          # builds into doc/web/site/
#   doc/web/build.sh serve    # serves at http://localhost:8000 with reload
#
# The version here and the one in requirements.txt, which the Pages workflow
# installs, should be the same.
#
# The object types page is copied from doc/types.md by prepare, once, before
# Zensical starts.  While serving, an edit to doc/types.md shows up only
# after running doc/web/prepare again.
set -e
cd "$(dirname "$0")"

./prepare

image=zensical/zensical:0.1.3

case "${1:-build}" in
  build)
    exec docker run --rm -v "$PWD":/docs "$image" build --clean --strict
    ;;
  serve)
    exec docker run --rm -it -p 8000:8000 -v "$PWD":/docs "$image" serve --dev-addr 0.0.0.0:8000
    ;;
  *)
    echo "usage: $0 [build|serve]" >&2
    exit 1
    ;;
esac
