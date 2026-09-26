package Jiggle::App::Command::ingest;
use v5.36;

use Jiggle::App -command;

use Jiggle::Derive;
use Jiggle::Ingest;
use Path::Tiny ();

sub abstract { 'bring new photos into the library' }

sub usage_desc { '%c ingest %o FILE-OR-DIR...' }

sub opt_spec {
  return (
    [ 'no-derive', "don't make renditions for the new photos yet" ],
  );
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('nothing to ingest') unless @$args;
}

sub execute ($self, $opt, $args) {
  my @files = sort map {;
    my $path = Path::Tiny::path($_);
    $path->is_dir ? (grep {; $_->is_file && $_->basename !~ /\A\./ } $path->children)
                  : $path
  } @$args;

  my $ingest = Jiggle::Ingest->new({
    library => $self->library,
    logger  => $self->logger,
  });

  my @photos = $ingest->ingest_files(@files);
  say sprintf 'ingested %d of %d file(s)', 0 + @photos, 0 + @files;

  return if $opt->no_derive or ! @photos;

  Jiggle::Derive->new({
    library => $self->library,
    logger  => $self->logger,
  })->derive_photos(@photos);
}

1;
