package Jiggle::App::Command::import_flickr;
use v5.36;

use Jiggle::App -command;

use Jiggle::Derive;
use Jiggle::Import::FlickrBackup;
use Jiggle::Library;

sub command_names { 'import-flickr' }

sub abstract { 'import a Net::Flickr::Backup archive' }

sub usage_desc { '%c import-flickr %o BACKUP-DIR' }

sub opt_spec {
  return (
    [ 'no-derive', "don't make renditions for the imported photos yet" ],
  );
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('give exactly one backup directory') unless @$args == 1;
  $self->usage_error("$args->[0] isn't a directory") unless -d $args->[0];
}

sub execute ($self, $opt, $args) {
  my $summary = Jiggle::Import::FlickrBackup->new({
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
