package Jiggle::App::Command::build;
use v5.36;

use Jiggle::App -command;

use Jiggle::Derive;
use Jiggle::Search;
use Jiggle::Site;

sub abstract { 'make any missing renditions, then render the site' }

sub opt_spec {
  return (
    [ 'jobs|j=i',  'how many renditions to make at once (default: one per CPU)' ],
    [ 'no-search', "don't build the search index (it needs Pagefind, via npx)" ],
  );
}

sub execute ($self, $opt, $args) {
  my $library = $self->library;

  # Private photos get renditions too, for local tools; the site builder is
  # what keeps them from being published.
  Jiggle::Derive->new({
    library => $library,
    logger  => $self->logger,
    ($opt->jobs ? (jobs => $opt->jobs) : ()),
  })->derive_photos($library->photos);

  Jiggle::Site->new({
    library => $library,
    logger  => $self->logger,
    search  => ($opt->no_search ? undef : Jiggle::Search->new({ logger => $self->logger })),
  })->build;
}

1;
