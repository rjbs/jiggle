package Jiggle::Sheet;
use v5.36;

use Moo;

use HTML::Entities ();
use JSON::MaybeXS ();
use Mojo::Util ();

=head1 NAME

Jiggle::Sheet - a local contact sheet of some photos, for looking things over

=head1 SYNOPSIS

  my $sheet = Jiggle::Sheet->new({ library => $library, label => 'private' });
  $path->spew_utf8($sheet->html(\@photos, { group => 'flickr-privacy' }));

=head1 DESCRIPTION

A contact sheet is one HTML page of thumbnails, each captioned with the
photo's title, date, albums, tags, and state (private, pending, video), and
its id, which links to its metadata file.  The images are the photos'
renditions in F<derived/>, by C<file://> URL, so the page works only on the
machine with the library, and it's never part of the site.

Photos can be grouped by C<year> taken, or by C<flickr-privacy>, the
privacy level they had on Flickr (from the raw record kept at import),
which is what reviewing private photos wants.

=cut

has library => (is => 'ro', required => 1);
has label   => (is => 'ro', default => '');

my %GROUPER = (
  year => sub ($self, $photo) {
    ($photo->taken // '') =~ /\A(\d{4})/ ? $1 : 'undated';
  },
  'flickr-privacy' => sub ($self, $photo) {
    my $id = $photo->flickr_id or return 'not from Flickr';
    my $record = $self->library->flickr_dir->child("$id.json");
    return 'unknown' unless -e $record;
    return JSON::MaybeXS::decode_json($record->slurp_raw)->{privacy} // 'unknown';
  },
);

sub groupings ($class) { sort keys %GROUPER }

my $h = sub ($s) { HTML::Entities::encode_entities($s // '', q{<>&"'}) };

# A file: URL for a path, escaped, since a library's path may have spaces.
sub _file_url ($path) { 'file://' . Mojo::Util::url_escape("$path", '^A-Za-z0-9\-._~/') }

=method html

  my $html = $sheet->html(\@photos, { group => 'year' });

This returns the page for the photos, in the order given (within each group,
if grouping).

=cut

sub html ($self, $photos, $arg = {}) {
  my $library = $self->library;

  my %albums_of;
  for my $album ($library->albums) {
    push $albums_of{$_}->@*, $album->title for $album->photos->@*;
  }

  my @groups;
  if (my $group = $arg->{group}) {
    my $grouper = $GROUPER{$group} or die "can't group by $group\n";
    my %photos_in;
    for my $photo (@$photos) {
      my $key = $self->$grouper($photo);
      push @groups, $key unless $photos_in{$key};
      push $photos_in{$key}->@*, $photo;
    }
    @groups = map {; [ $_, $photos_in{$_} ] } sort @groups;
  } else {
    @groups = [ undef, $photos ];
  }

  my @sections;
  for my $group (@groups) {
    my ($name, $members) = @$group;
    my @tiles = map {; $self->_tile($_, $albums_of{ $_->id } // []) } @$members;
    push @sections,
      (defined $name ? sprintf(qq{<h2>%s <span>%d</span></h2>\n}, $h->($name), 0 + @$members) : '')
      . qq{<div class="grid">\n} . join("\n", @tiles) . qq{\n</div>};
  }

  my $total = @$photos;
  my $title = length $self->label ? $self->label : 'photos';

  return <<~"END";
    <!doctype html>
    <html lang="en">
    <head>
    <meta charset="utf-8">
    <title>@{[ $h->($title) ]} ($total)</title>
    <style>
      :root { color-scheme: light dark; --muted: #777; }
      body { font: 14px/1.4 system-ui, sans-serif; margin: 1.5rem; }
      h1 { font-size: 1.4rem; }
      h2 { font-size: 1.1rem; margin-top: 2rem; }
      h1 span, h2 span, figcaption .id { color: var(--muted); font-weight: normal; }
      p { color: var(--muted); max-width: 45em; }
      .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 1rem; }
      figure { margin: 0; }
      figure img { width: 100%; height: 180px; object-fit: contain; background: rgba(128,128,128,.12); }
      figcaption { font-size: 12px; margin-top: 4px; overflow-wrap: anywhere; }
      .flag { font-size: 10px; font-weight: 600; text-transform: uppercase; padding: 0 4px; border-radius: 3px;
              background: rgba(128,128,128,.2); }
      .flag.pending { background: rgba(217,119,6,.3); }
      .none { height: 180px; display: grid; place-items: center; color: var(--muted); background: rgba(128,128,128,.12); }
    </style>
    </head>
    <body>
    <h1>@{[ $h->($title) ]} <span>$total</span></h1>
    <p>Each id links to the photo's metadata file; the image links to a larger
    rendition.  This page reads the library directly, so it works only on this
    machine, and is never published.</p>
    @{[ join "\n", @sections ]}
    </body>
    </html>
    END
}

sub _tile ($self, $photo, $albums) {
  my $library = $self->library;
  my $thumb = $library->derived_path($photo->id, 'h480.webp');
  my $large = $library->derived_path($photo->id, '1024.webp');
  my $meta  = $library->meta_path($photo->id);

  my @flags = (
    ($photo->is_public ? () : 'private'),
    ($photo->pending   ? 'pending' : ()),
    ($photo->is_video  ? 'video'   : ()),
  );

  return join '',
    qq{<figure>},
    (-e $thumb
      ? sprintf(q{<a href="%s"><img src="%s" loading="lazy" alt=""></a>}, _file_url($large), _file_url($thumb))
      : qq{<div class="none">no rendition</div>}),
    qq{<figcaption><b>}, $h->(length $photo->title ? $photo->title : '(untitled)'), qq{</b><br>},
    $h->(substr($photo->taken // 'undated', 0, 10)),
    (map {; qq{ <span class="flag $_">$_</span>} } @flags),
    (@$albums ? '<br><i>' . $h->(join '; ', @$albums) . '</i>' : ''),
    ($photo->tags->@* ? '<br>' . $h->(join ', ', $photo->tags->@*) : ''),
    sprintf(q{<br><a class="id" href="%s">%s</a></figcaption></figure>}, _file_url($meta), $photo->id);
}

1;
