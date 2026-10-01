package Jiggle::App::Command::remove;
use v5.36;

use Jiggle::App -command;

use Jiggle::Remove;

sub abstract { 'take photos out of the library' }

sub usage_desc { '%c remove %o ID...' }

sub description {
  return <<~'END';
  This deletes each photo's metadata, takes it out of its albums, and deletes
  its renditions, committing the changes to meta/ if it's a git repository.

  The original is kept, so reverting that commit undoes the removal.  With
  --remove-original, the original is deleted too, and there's no undoing it
  (except from a backup).

  END
}

sub opt_spec {
  return (
    [ 'remove-original', 'delete the original files too; this cannot be undone' ],
  );
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('no photos given') unless @$args;
}

sub execute ($self, $opt, $args) {
  my $result = Jiggle::Remove->new({
    library => $self->library,
    logger  => $self->logger,
  })->remove($args, { originals => $opt->remove_original });

  say 'removed ', $_->id for $result->{removed}->@*;
  say "deleted $_" for $result->{originals}->@*;
  say sprintf 'changed %d album(s): %s', 0 + $result->{albums}->@*, join q{, }, $result->{albums}->@*
    if $result->{albums}->@*;
  say "committed $result->{commit} in meta/" if $result->{commit};

  if (my @published = $result->{published}->@*) {
    say sprintf 'warning: %d of these were published (%s); the next build and sync take '
      . 'their pages down, and any links or blog embeds of them will break',
      0 + @published, join q{ }, @published;
  }
}

1;
