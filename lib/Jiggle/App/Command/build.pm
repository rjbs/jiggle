package Jiggle::App::Command::build;
use v5.36;

use Jiggle::App -command;

use Jiggle::Derive;
use Jiggle::Search;
use Jiggle::Site;

sub abstract { 'make any missing renditions, then render the site' }

sub description {
  <<~'END';
  A build trusts its manifests, which record what earlier builds made, so a
  build in which little changed does little work.  A file changed or deleted
  by hand in derived/ or site/ isn't noticed; --verify checks everything on
  disk instead, and rebuilds the manifests.
  END
}

sub opt_spec {
  return (
    [ 'jobs|j=i',  'how many renditions to make at once (default: one per CPU)' ],
    [ 'no-search', "don't build the search index (it needs Pagefind, via npx)" ],
    [ 'verify',    'check files on disk instead of trusting the manifests' ],
  );
}

sub execute ($self, $opt, $args) {
  my $library = $self->library;

  # Private photos get renditions too, for local tools; the site builder is
  # what keeps them from being published.
  my $derive = Jiggle::Derive->new({
    library => $library,
    logger  => $self->logger,
    verify  => $opt->verify,
    ($opt->jobs ? (jobs => $opt->jobs) : ()),
  });

  $derive->derive_photos($library->photos);

  Jiggle::Site->new({
    library => $library,
    logger  => $self->logger,
    derive  => $derive,
    verify  => $opt->verify,
    search  => ($opt->no_search ? undef : Jiggle::Search->new({ logger => $self->logger })),
  })->build;
}

1;
