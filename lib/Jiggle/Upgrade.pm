package Jiggle::Upgrade;
use v5.36;

use Moo;

use Jiggle::Library;
use Jiggle::Photo;
use Jiggle::TOML qw( load_toml_file );

=head1 NAME

Jiggle::Upgrade - bring a library up to the current format

=head1 SYNOPSIS

  my $library = Jiggle::Library->new({ root => $root, allow_old_format => 1 });
  my @done = Jiggle::Upgrade->new({ library => $library })->upgrade;

=head1 DESCRIPTION

A library's format (see L<Jiggle::Library/format>) changes when its layout or
metadata does.  Upgrading runs each step from the library's format to the
current one, committing what each step changes in F<meta>, and then records
the new format in F<jiggle.toml>, last, so that an upgrade interrupted
partway is finished by running it again.

=cut

has library => (is => 'ro', required => 1);

# Each step moves a library from the format it's named for to the next, and
# returns a description of what it did.
my %STEP = (
  1 => \&_pending_becomes_a_visibility,
);

=method upgrade

This upgrades the library, returning a description of each step taken, or
nothing if the library was already current.

=cut

sub upgrade ($self) {
  my $library = $self->library;
  my $format  = $library->format;

  my @done;
  while ($format < $Jiggle::Library::FORMAT) {
    my $step = $STEP{$format} or die "no way to upgrade from format $format\n";
    push @done, sprintf 'format %d to %d: %s', $format, $format + 1, $self->$step;
    $format++;
  }

  $self->_record_format($format) if @done;
  return @done;
}

# The format is changed in place, so the rest of jiggle.toml (its comments,
# say) is left as it was.
sub _record_format ($self, $format) {
  my $file = $self->library->root->child('jiggle.toml');
  my $toml = $file->slurp_utf8;
  $toml = "format = $format\n$toml" unless $toml =~ s/^(format\s*=\s*)[0-9]+/$1$format/m;
  $file->spew_utf8($toml);
}

# Format 2: pending was a key of its own (pending = true), beside a
# visibility; now it's a visibility.  A pending photo becomes pending,
# whatever its visibility was, since it hasn't been reviewed.
sub _pending_becomes_a_visibility ($self) {
  my $library = $self->library;

  my (@changed, $pending);
  for my $shard ($library->meta_dir->children) {
    next unless $shard->is_dir;
    next if $shard->basename =~ /\A(?:albums|flickr|\..*)\z/;

    for my $file ($shard->children(qr/\.toml\z/)) {
      my $data = load_toml_file($file);
      next unless exists $data->{pending};

      if (delete $data->{pending}) {
        $data->{visibility} = 'pending';
        $pending++;
      }
      $file->spew_utf8(Jiggle::Photo->new($data)->as_toml);
      push @changed, $file;
    }
  }

  return 'no photo had a pending key' unless @changed;

  my $what = sprintf '%d photo(s) were pending', $pending // 0;
  $library->commit_meta(
    "upgrade the library to format 2: pending is a visibility\n\n$what.",
    @changed,
  );
  return "$what; now their visibility is pending";
}

1;
