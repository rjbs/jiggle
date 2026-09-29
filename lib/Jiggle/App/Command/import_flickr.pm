package Jiggle::App::Command::import_flickr;
use v5.36;

use Jiggle::App -command;

use Jiggle::Derive;
use Jiggle::Import::FlickrBackup;
use Jiggle::Import::FlickrExport;
use Jiggle::Library;

sub command_names { 'import-flickr' }

sub abstract { 'import a Flickr data export, or a Net::Flickr::Backup archive' }

sub usage_desc { '%c import-flickr %o DIR' }

sub description {
  <<~'END';
  DIR is either Flickr's own data export, unpacked (with metadata/ and photos/
  directories inside, one subdirectory per zip), or a Net::Flickr::Backup
  archive.  The export is the better source; see Jiggle::Import::FlickrExport.
  END
}

sub opt_spec {
  return (
    [ 'no-derive', "don't make renditions for the imported photos yet" ],
    [ 'force',     'import even into a library whose meta/ has git commits' ],
  );
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('give exactly one directory') unless @$args == 1;
  $self->usage_error("$args->[0] isn't a directory") unless -d $args->[0];
}

# Once meta/ is committed, it's the source of truth, and may have been edited
# by hand.  Importing again skips photos already present, but rewrites every
# album from Flickr's data (title, description, order, cover), which would
# undo those edits.  -- claude, 2026-09-28
sub _meta_is_committed ($self) {
  my $meta = $self->library->meta_dir;
  return 0 unless -e $meta->child('.git');

  # \Q quotes the path for the shell; library paths may well have spaces.
  qx{git -C \Q$meta\E rev-parse --quiet --verify HEAD 2>&1};
  return $? == 0;
}

sub execute ($self, $opt, $args) {
  if (! $opt->force and $self->_meta_is_committed) {
    die <<~'END';
    This library's meta/ has git commits, so it may have been edited by hand,
    and importing again would overwrite every album from Flickr's data.  If
    that's what you want, use --force, and review the result with git diff.
    END
  }

  my $class = Jiggle::Import::FlickrExport->looks_like_export($args->[0])
            ? 'Jiggle::Import::FlickrExport'
            : 'Jiggle::Import::FlickrBackup';

  $self->logger->("importing with $class");

  my $summary = $class->new({
    library => $self->library,
    root    => $args->[0],
    logger  => $self->logger,
  })->run;

  say sprintf 'imported %d, already present %d, skipped %d; wrote %d album(s)',
    @$summary{qw( imported existing )}, scalar $summary->{skipped}->@*, $summary->{albums};
  say "  skipped $_" for $summary->{skipped}->@*;

  return if $opt->no_derive;

  # A fresh library object, so it sees everything just imported.
  my $library = Jiggle::Library->new({ root => $self->library->root });
  Jiggle::Derive->new({ library => $library, logger => $self->logger })
    ->derive_photos($library->photos);
}

1;
