#!/bin/sh
# Builds or serves the site with imprint, which must be checked out beside
# this repository, as semiotic-imprint.  See its bin/build-site.
#
#   doc/web/build.sh          # builds into doc/web/site/
#   doc/web/build.sh serve    # serves at http://localhost:8000 with reload
exec "$(dirname "$0")/../../../semiotic-imprint/bin/build-site" "$(dirname "$0")" "$@"
