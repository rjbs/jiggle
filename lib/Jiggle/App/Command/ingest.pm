package Jiggle::App::Command::ingest;
use v5.36;

use Jiggle::App -command;

use Jiggle::Derive;
use Jiggle::Editor;
use Jiggle::Ingest;
use Path::Tiny ();

sub abstract { 'bring new photos into the library' }

sub usage_desc { '%c ingest %o FILE-OR-DIR...' }

sub opt_spec {
  return (
    [ 'no-derive', "don't make renditions for the new photos yet" ],
    [ 'edit',      'then review everything pending in the editor' ],
    [ 'port|p=i',  'with --edit, the port for the editor', { default => 3001 } ],
  );
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('nothing to ingest') unless @$args;

  # The editor shows renditions, so photos without them would be blank.
  $self->usage_error("--edit needs renditions, so it can't go with --no-derive")
    if $opt->edit and $opt->no_derive;
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

  Jiggle::Derive->new({
    library => $self->library,
    logger  => $self->logger,
  })->derive_photos(@photos) if @photos and ! $opt->no_derive;

  return unless $opt->edit;

  Jiggle::Editor->serve_query($self->library, [ 'pending' ], { port => $opt->port, open => 1 });
}

1;
